#!/usr/bin/env python3
"""Build immutable, inspectable Copilot Ready release artifacts. No user data is read."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess
from urllib.parse import urlsplit
import zipfile

BINARIES = {
    ('windows', 'amd64'): 'copilot-ready-windows-amd64.exe',
    ('windows', 'arm64'): 'copilot-ready-windows-arm64.exe',
    ('darwin', 'amd64'): 'copilot-ready-darwin-amd64',
    ('darwin', 'arm64'): 'copilot-ready-darwin-arm64',
}

def validate_base_url(value):
    parsed = urlsplit(value)
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password
        or parsed.query or parsed.fragment or parsed.path not in ('', '/')
        or not re.fullmatch(r'https://[A-Za-z0-9.-]+(?::[0-9]+)?/?', value)):
        raise ValueError('Base URL must be a plain HTTPS origin without credentials, query, or path.')
    return value.rstrip('/')

def validate_version(version):
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', version):
        raise ValueError('Version must be a semantic version.')
    if any(int(n) > 0xFFFF for n in version.split('-')[0].split('.')):
        raise ValueError('Each version number must fit Windows version information (65535 or less).')
    return version

def sha256(path):
    with open(path, 'rb') as file:
        return hashlib.file_digest(file, 'sha256').hexdigest()

# Each Windows download is a ZIP holding the helper for one processor and a plain PowerShell
# script that runs it. Nothing is downloaded or hidden at run time, and the script uses only
# cmdlets, so it also runs where PowerShell is limited to Constrained Language Mode.
WINDOWS_ZIPS = {'amd64': 'Install-Copilot-Windows.zip', 'arm64': 'Install-Copilot-Windows-ARM64.zip'}

def windows_script(arch, digest):
    ps = r"""# Copilot Ready setup for Windows. This is a plain script: you can read it before running it.
# It checks that the Copilot Ready helper in this folder isn't damaged (its SHA-256 matches the
# one published for this release), then runs it. It downloads nothing and changes no settings.
# To run it: right-click this file and choose Run with PowerShell
# (on Windows 11, choose Show more options first).

$ErrorActionPreference = 'Stop'
$blocked = 'This usually means antivirus or other security software blocked it. Check Windows Security > Virus & threat protection > Protection history, or ask your IT team to allow Copilot Ready.'

function Stop-Setup($message) {
  Write-Host ''
  Write-Host $message -ForegroundColor Red
  Write-Host ''
  Read-Host 'Press Enter to close' | Out-Null
  exit 1
}

$download = '__ARCH__'  # the processor this ZIP's helper is built for
$cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
if ($cpu -ne 'AMD64' -and $cpu -ne 'ARM64') { Stop-Setup 'Copilot Ready supports Windows on x64 and ARM64 processors.' }
if ($download -eq 'ARM64' -and $cpu -ne 'ARM64') { Stop-Setup 'This download is for ARM PCs. Download Copilot Ready for Windows x64 from the website instead.' }
if ($download -eq 'AMD64' -and $cpu -eq 'ARM64') { Write-Host 'This PC has an ARM processor. This x64 download works, but the ARM64 download from the website runs faster.' }

$file = Join-Path $PSScriptRoot 'copilot-ready.exe'
if (-not (Test-Path -LiteralPath $file)) {
  if (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'README.txt'))) {
    Stop-Setup 'Extract the whole ZIP first: right-click it and choose Extract All. Then run Install Copilot.ps1 from the extracted folder.'
  }
  Stop-Setup ('copilot-ready.exe is missing from this folder. ' + $blocked)
}

# Security software may hold a new file while it scans it, so reading is retried for a few seconds.
$actual = $null
for ($attempt = 1; $attempt -le 5 -and -not $actual; $attempt++) {
  try { $actual = (Get-FileHash -LiteralPath $file -Algorithm SHA256 -ErrorAction Stop).Hash }
  catch { if ($attempt -lt 5) { Start-Sleep -Seconds 2 } }
}
if (-not $actual) { Stop-Setup ("Windows didn't let setup read copilot-ready.exe. " + $blocked) }
if ($actual -ne '__DIGEST__') { Stop-Setup "copilot-ready.exe is damaged or doesn't match this release, so it wasn't run. Download Copilot Ready again." }

Write-Host 'Copilot Ready - opening your local setup wizard...'
try { & $file } catch { Stop-Setup ("Windows didn't let copilot-ready.exe run. " + $blocked) }
if ($LASTEXITCODE -ne 0) { Stop-Setup 'Setup did not finish. Review its message above, then run Install Copilot.ps1 again.' }
"""
    ps = ps.replace('__ARCH__', {'amd64': 'AMD64', 'arm64': 'ARM64'}[arch]).replace('__DIGEST__', digest)
    # Windows PowerShell 5.1 reads a script without a byte order mark as the system code page.
    ps.encode('ascii')
    return ps.replace('\n', '\r\n')

WINDOWS_README = """Copilot Ready for Windows

1. Extract this ZIP: right-click it and choose Extract All.
2. In the extracted folder, right-click Install Copilot.ps1 and choose Run with PowerShell.
   On Windows 11, choose Show more options first.
   Your setup opens in your browser. Keep the PowerShell window open until it finishes.

If the PowerShell window closes straight away without opening setup, your PC only allows
signed scripts. Open copilot-ready.exe from the same folder instead.

The helper isn't code-signed, so Windows may say it protected your PC.
Choose More info, then Run anyway. If antivirus blocks it, check
Windows Security > Virus & threat protection > Protection history.

Install Copilot.ps1 checks copilot-ready.exe against the SHA-256 in this ZIP, which catches a
damaged file. To check the ZIP itself, compare its SHA-256 with downloads/manifest.json on the website.
""".replace('\n', '\r\n')

def mac_script(base, version, hashes):
    script = '''#!/bin/bash
set -euo pipefail
umask 077
TEMP_DIR=""
TEMP_BASE="${TMPDIR:-/tmp}"
TEMP_BASE="${TEMP_BASE%/}"
finish() {
  status=$?
  if [[ -n "$TEMP_DIR" && "$TEMP_DIR" == "$TEMP_BASE"/copilot-ready.* && -d "$TEMP_DIR" && ! -L "$TEMP_DIR" ]]; then /bin/rm -rf -- "$TEMP_DIR"; fi
  if [[ "$status" != 0 ]]; then printf '\\nSetup stopped. Review the message above, then reopen this download to retry.\\nPress Return to close.'; read -r _ || true; fi
}
trap finish EXIT
[[ "$(/usr/bin/uname -s)" == Darwin ]] || { printf 'This download supports macOS only.\\n'; exit 1; }
case "$(/usr/bin/uname -m)" in
  arm64) ARCH=arm64;;
  x86_64) if [[ "$(/usr/sbin/sysctl -in sysctl.proc_translated 2>/dev/null || true)" == 1 ]]; then ARCH=arm64; else ARCH=amd64; fi;;
  *) printf 'Unsupported processor.\\n'; exit 1;;
esac
if [[ "$ARCH" == arm64 ]]; then DIGEST=__ARM64__; else DIGEST=__AMD64__; fi
printf '\\nCopilot Ready - opening your local setup wizard...\\n'
TEMP_DIR=$(/usr/bin/mktemp -d "$TEMP_BASE/copilot-ready.XXXXXX")
FILE="$TEMP_DIR/copilot-ready"
/usr/bin/curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 --retry 3 --connect-timeout 30 --max-time 600 '__BASE__/downloads/__VERSION__/copilot-ready-darwin-'"$ARCH" -o "$FILE"
ACTUAL=$(/usr/bin/shasum -a 256 "$FILE"); ACTUAL=${ACTUAL%% *}
[[ "$ACTUAL" == "$DIGEST" ]] || { printf 'Download integrity check failed. Nothing was executed.\\n'; exit 1; }
/bin/chmod 700 "$FILE"
"$FILE"
'''
    return (script.replace('__BASE__', base).replace('__VERSION__', version)
            .replace('__AMD64__', hashes[('darwin','amd64')]).replace('__ARM64__', hashes[('darwin','arm64')]))

def zip_tree(source, target):
    with zipfile.ZipFile(target, 'w', zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(source.rglob('*')):
            if path.is_file() and not path.is_symlink():
                archive.write(path, path.relative_to(source).as_posix())

# Windows shows a program's version information in its properties, and security software weighs it:
# an executable without any looks anonymous. Go links a .syso object file found next to the package,
# so the build writes one per processor holding the version and an application manifest. The format
# is a COFF object with one .rsrc section; see "PE Format" (Microsoft Learn) and VS_VERSIONINFO.
WINDOWS_MANIFEST = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="CopilotReady.Setup" version="__VERSION__"/>
  <description>Copilot Ready setup helper</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges><requestedExecutionLevel level="asInvoker" uiAccess="false"/></requestedPrivileges></security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application><supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/></application>
  </compatibility>
</assembly>
"""
COFF_MACHINE = {'amd64': (0x8664, 3), 'arm64': (0xAA64, 2)}  # machine, ADDR32NB relocation type

def version_numbers(version):
    return [int(n) for n in re.match(r'([0-9]+)\.([0-9]+)\.([0-9]+)', validate_version(version)).groups()] + [0]

def version_node(key, value=b'', children=(), text=False):
    """One VS_VERSIONINFO node: header, key, value and children, each starting on a 4-byte boundary."""
    data = struct.pack('<HHH', 0, len(value) // 2 if text else len(value), 1 if text or not value else 0)
    data += (key + '\0').encode('utf-16-le')
    data += b'\0' * (-len(data) % 4) + value
    for child in children:
        data += b'\0' * (-len(data) % 4) + child
    return struct.pack('<H', len(data)) + data[2:]

def version_info(version):
    major, minor, patch, build = version_numbers(version)
    ms, ls = major << 16 | minor, patch << 16 | build
    fixed = struct.pack('<13I', 0xFEEF04BD, 0x00010000, ms, ls, ms, ls, 0x3F, 0, 0x40004, 1, 0, 0, 0)
    strings = {'FileDescription': 'Copilot Ready setup helper', 'FileVersion': version, 'InternalName': 'copilot-ready',
               'OriginalFilename': 'copilot-ready.exe', 'ProductName': 'Copilot Ready', 'ProductVersion': version}
    table = version_node('040904b0', children=[version_node(k, (v + '\0').encode('utf-16-le'), text=True) for k, v in strings.items()])
    translation = version_node('Translation', struct.pack('<HH', 0x0409, 0x04B0))
    return version_node('VS_VERSION_INFO', fixed, [version_node('StringFileInfo', children=[table]), version_node('VarFileInfo', children=[translation])])

def windows_resources(version, arch):
    """A COFF object whose .rsrc section holds the version information and the manifest (both ID 1, US English)."""
    machine, addr32nb = COFF_MACHINE[arch]
    resources = [(16, version_info(version)), (24, WINDOWS_MANIFEST.replace('__VERSION__', '.'.join(map(str, version_numbers(version)))).encode('utf-8'))]
    directory = lambda count: struct.pack('<IIHHHH', 0, 0, 0, 0, 0, count)
    n = len(resources)
    # Root directory, then for each type a name directory and a language directory, then data entries.
    names_at = 16 + 8 * n
    languages_at = names_at + n * 24
    entries_at = languages_at + n * 24
    data_at = entries_at + n * 16
    tree, relocations, blobs = directory(n), [], b''
    for i, (kind, _) in enumerate(resources):
        tree += struct.pack('<II', kind, 0x80000000 | names_at + 24 * i)
    for i in range(n):
        tree += directory(1) + struct.pack('<II', 1, 0x80000000 | languages_at + 24 * i)
    for i in range(n):
        tree += directory(1) + struct.pack('<II', 0x0409, entries_at + 16 * i)
    for i, (_, blob) in enumerate(resources):
        relocations.append(len(tree))
        tree += struct.pack('<IIII', data_at + len(blobs), len(blob), 0, 0)
        blobs += blob + b'\0' * (-len(blob) % 8)
    section = tree + blobs
    relocations_at = 20 + 40 + len(section)
    symbols_at = relocations_at + 10 * len(relocations)
    out = struct.pack('<HHIIIHH', machine, 1, 0, symbols_at, 1, 0, 0)
    out += struct.pack('<8sIIIIIIHHI', b'.rsrc', 0, 0, len(section), 60, relocations_at, 0, len(relocations), 0, 0x40000040)
    out += section
    for offset in relocations:
        out += struct.pack('<IIH', offset, 0, addr32nb)
    out += struct.pack('<8sIhHBB', b'.rsrc', 0, 1, 0, 3, 0) + struct.pack('<I', 4)
    return out

def build(root, base, version):
    folder = root / 'build'
    folder.mkdir(exist_ok=True)
    package = root / 'cmd' / 'copilot-ready'
    resources = [package / f'rsrc_windows_{arch}.syso' for arch in COFF_MACHINE]
    try:
        for path, arch in zip(resources, COFF_MACHINE):
            path.write_bytes(windows_resources(version, arch))
        for (goos, arch), name in BINARIES.items():
            env = dict(os.environ, GOOS=goos, GOARCH=arch, CGO_ENABLED='0')
            subprocess.run(['go','build','-trimpath','-ldflags',f'-s -w -X main.releaseBaseURL={base} -X main.version={version}',
                            '-o',str(folder / name),'./cmd/copilot-ready'], cwd=root, env=env, check=True)
    finally:
        for path in resources:
            path.unlink(missing_ok=True)

def package(root, out, base, version, source_commit=None):
    base = validate_base_url(base)
    validate_version(version)
    out.mkdir(parents=True, exist_ok=True)
    if any(out.iterdir()):
        raise ValueError('Release output must be empty. Use a new directory to avoid stale artifacts.')
    shutil.copytree(root / 'site', out, dirs_exist_ok=True)
    if (root / 'server.cjs').exists():
        shutil.copyfile(root / 'server.cjs', out / 'server.cjs')
    downloads = out / 'downloads'
    assets = downloads / version
    assets.mkdir(parents=True, exist_ok=True)
    hashes = {}
    for platform, name in BINARIES.items():
        shutil.copyfile(root / 'build' / name, assets / name)
        hashes[platform] = sha256(assets / name)
    for arch, zip_name in WINDOWS_ZIPS.items():
        ps = windows_script(arch, hashes[('windows', arch)])
        with zipfile.ZipFile(downloads / zip_name,'w',zipfile.ZIP_DEFLATED) as archive:
            archive.writestr('Install Copilot.ps1', ps)
            archive.writestr('README.txt', WINDOWS_README)
            archive.write(assets / BINARIES[('windows', arch)], 'copilot-ready.exe')
        if arch == 'amd64':
            (downloads / 'Install-Copilot-readable.ps1').write_bytes(ps.encode('ascii'))
    mac = mac_script(base, version, hashes)
    (downloads / 'Install-Copilot-readable.command').write_text(mac, encoding='utf-8', newline='\n')
    with zipfile.ZipFile(downloads / 'Install-Copilot-macOS.zip','w',zipfile.ZIP_DEFLATED) as archive:
        info = zipfile.ZipInfo('Install Copilot.command')
        info.create_system = 3
        info.external_attr = (0o100755 << 16)
        info.compress_type = zipfile.ZIP_DEFLATED
        archive.writestr(info, mac)
    if (root / 'docs' / 'scaffold').exists():
        zip_tree(root / 'docs' / 'scaffold', downloads / 'copilot-ready-scaffold.zip')
    with zipfile.ZipFile(downloads / 'copilot-ready-source.zip','w',zipfile.ZIP_DEFLATED) as archive:
        for name in ['cmd','internal','website','site','docs','scripts','vendor','go.mod','go.sum','package.json','package-lock.json','README.md','server.cjs']:
            path = root / name
            candidates = sorted(path.rglob('*')) if path.is_dir() else [path]
            for item in candidates:
                if item.is_file() and not item.is_symlink() and '__pycache__' not in item.parts and item.suffix not in ('.log','.pyc'):
                    archive.write(item,item.relative_to(root).as_posix())
    manifest = {
        'schemaVersion': 1, 'releaseVersion': version, 'cliVersion': '1.0.105',
        'createdAt': datetime.now(timezone.utc).isoformat(), 'baseURL': base,
        **({'sourceCommit': source_commit} if source_commit else {}),
        'signing': {'bootstrap':'unsigned','helper':'unsigned','officialCLI':'verified official release digest; Windows official binary is Authenticode signed'},
        'validation': {'status':'candidate','windowsX64':'See docs/validation.md for versioned installation and regression evidence','liveMacOS':'not verified','liveWindowsARM64':'not verified','authenticatedCopilot':'not verified'},
        'artifacts': [],
    }
    for path in sorted(out.rglob('*')):
        if path.is_file() and path.name != 'server.cjs':
            manifest['artifacts'].append({'path':path.relative_to(out).as_posix(),'size':path.stat().st_size,'sha256':sha256(path)})
    (downloads / 'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
    return manifest

def source_commit(root):
    """The exact commit the release is built from; a dirty tree is refused so artifacts stay traceable."""
    try:
        head = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=root, check=True, capture_output=True, text=True).stdout.strip()
        dirty = subprocess.run(['git', 'status', '--porcelain', '--untracked-files=no'], cwd=root, check=True, capture_output=True, text=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return None
    if dirty:
        raise ValueError('Commit or stash changes before building a release so sourceCommit matches the artifacts.')
    return head

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', required=True)
    parser.add_argument('--version', default='2.0.6')
    parser.add_argument('--out', type=Path, default=Path('release'))
    parser.add_argument('--skip-build', action='store_true')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    base = validate_base_url(args.base_url)
    validate_version(args.version)
    out = args.out.resolve()
    if (root / 'website').exists():
        subprocess.run(['npm.cmd' if os.name == 'nt' else 'npm', 'run', 'build:web'], cwd=root, check=True)
    if not args.skip_build:
        build(root,base,args.version)
    result = package(root,out,base,args.version,source_commit(root))
    print(json.dumps({'output':str(out),'release':args.version,'artifacts':len(result['artifacts'])}))

if __name__ == '__main__':
    main()
