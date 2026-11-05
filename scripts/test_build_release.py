import importlib.util
import json
import struct
from pathlib import Path
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location('builder', Path(__file__).with_name('build_release.py'))
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class ReleaseTests(unittest.TestCase):
    def test_rejects_non_https_or_command_interpolating_origins(self):
        for value in ['http://example.test', 'https://example.test/path', "https://bad.test/'$(whoami)", 'https://user:pass@example.test', 'https://example.test?x=1']:
            with self.assertRaises(ValueError, msg=value):
                builder.validate_base_url(value)
        self.assertEqual(builder.validate_base_url('https://example.azurewebsites.net/'), 'https://example.azurewebsites.net')

    def test_mac_archive_preserves_executable_script_and_hashes_include_all_downloads(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'site').mkdir()
            (root / 'site' / 'index.html').write_text('<h1>site</h1>')
            (root / 'docs').mkdir()
            (root / 'docs' / 'guide.md').write_text('generic guide')
            (root / 'build').mkdir()
            for name in builder.BINARIES.values():
                (root / 'build' / name).write_bytes(b'fixture executable')
            out = root / 'release'
            builder.package(root, out, 'https://example.azurewebsites.net', '2.0.0')
            with zipfile.ZipFile(out / 'downloads' / 'Install-Copilot-macOS.zip') as archive:
                script = archive.getinfo('Install Copilot.command')
                self.assertEqual((script.external_attr >> 16) & 0o777, 0o755)
            manifest = json.loads((out / 'downloads' / 'manifest.json').read_text())
            names = {item['path'] for item in manifest['artifacts']}
            self.assertIn('downloads/Install-Copilot-Windows.zip', names)
            self.assertIn('downloads/Install-Copilot-Windows-ARM64.zip', names)
            self.assertNotIn('downloads/Install-Copilot.cmd', names)
            self.assertIn('downloads/Install-Copilot-macOS.zip', names)
            self.assertIn('downloads/copilot-ready-source.zip', names)
            self.assertNotIn('downloads/manifest.json', names)
            for item in manifest['artifacts']:
                self.assertEqual(builder.sha256(out / item['path']), item['sha256'])

    def test_each_windows_zip_runs_its_local_helper_with_a_plain_script(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'site').mkdir()
            (root / 'site' / 'index.html').write_text('site')
            (root / 'build').mkdir()
            for (goos, arch), name in builder.BINARIES.items():
                (root / 'build' / name).write_bytes(f'{goos} {arch} helper'.encode())
            out = root / 'release'
            builder.package(root, out, 'https://example.azurewebsites.net', '2.0.0')
            for arch, zip_name in builder.WINDOWS_ZIPS.items():
                with zipfile.ZipFile(out / 'downloads' / zip_name) as archive:
                    self.assertEqual(sorted(archive.namelist()), ['Install Copilot.ps1', 'README.txt', 'copilot-ready.exe'])
                    self.assertEqual(archive.read('copilot-ready.exe'), f'windows {arch} helper'.encode())
                    text = archive.read('Install Copilot.ps1').decode('ascii')
                self.assertNotIn('\n', text.replace('\r\n', ''))
                self.assertIn(f"$download = '{arch.upper()}'", text)
                self.assertIn(builder.sha256(out / 'downloads' / '2.0.0' / builder.BINARIES[('windows', arch)]), text)
                # Only cmdlets: no .NET calls, type loading or downloads, so Constrained Language Mode allows it.
                for word in ['EncodedCommand', 'Add-Type', '::', 'New-Object', 'Invoke-WebRequest', 'http', 'Set-ExecutionPolicy', 'GetTempPath', '__']:
                    self.assertNotIn(word, text)
            readable = (out / 'downloads' / 'Install-Copilot-readable.ps1').read_bytes()
            with zipfile.ZipFile(out / 'downloads' / 'Install-Copilot-Windows.zip') as archive:
                self.assertEqual(readable, archive.read('Install Copilot.ps1'))
            self.assertFalse((out / 'downloads' / 'Install-Copilot.cmd').exists())

    def test_version_is_checked_before_anything_is_built(self):
        for bad in ['v2.1', '2.1', '2.0.0; rm', '', '70000.0.0', '2.0.65536-rc.1']:
            with self.assertRaises(ValueError, msg=bad):
                builder.validate_version(bad)
            with self.assertRaises(ValueError, msg=bad):
                builder.version_numbers(bad)
        self.assertEqual(builder.validate_version('2.0.11-rc.1'), '2.0.11-rc.1')

    def test_windows_resources_carry_version_and_manifest_for_each_processor(self):
        for arch, (machine, relocation) in builder.COFF_MACHINE.items():
            obj = builder.windows_resources('2.0.11-rc.1', arch)
            self.assertEqual(struct.unpack_from('<HH', obj, 0), (machine, 1))
            name, _, _, size, raw, relocations_at, _, count = struct.unpack_from('<8sIIIIIIH', obj, 20)
            self.assertEqual((name.rstrip(b'\0'), raw, count), (b'.rsrc', 60, 2))
            section = obj[raw:raw + size]
            for i in range(count):
                offset, symbol, kind = struct.unpack_from('<IIH', obj, relocations_at + 10 * i)
                data_at, data_size = struct.unpack_from('<II', section, offset)
                self.assertEqual((symbol, kind), (0, relocation))
                self.assertLessEqual(data_at + data_size, size)
            self.assertIn('Copilot Ready setup helper'.encode('utf-16-le'), section)
            self.assertIn(b'version="2.0.11.0"', section)
            self.assertIn(b'level="asInvoker"', section)
        info = builder.version_info('2.0.11')
        self.assertEqual(struct.unpack_from('<HHH', info, 0), (len(info), 52, 0))
        self.assertEqual(struct.unpack_from('<4I', info, 40), (0xFEEF04BD, 0x00010000, 2 << 16 | 0, 11 << 16))
        with self.assertRaises(ValueError):
            builder.version_numbers('70000.0.0')

    def test_packaging_does_not_include_private_or_build_state(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'site').mkdir()
            (root / 'site' / 'index.html').write_text('site')
            (root / 'docs').mkdir()
            (root / 'build').mkdir()
            (root / '.git').mkdir()
            (root / '.git' / 'config').write_text('private credential')
            (root / 'personal-profile.json').write_text('secret')
            for name in builder.BINARIES.values():
                (root / 'build' / name).write_bytes(b'fixture')
            (root / 'go.mod').write_text('module example')
            (root / 'website').mkdir()
            (root / 'website' / 'App.jsx').write_text('export default function App() {}')
            (root / 'package-lock.json').write_text('{}')
            (root / 'node_modules').mkdir()
            (root / 'node_modules' / 'local.txt').write_text('dependency state')
            builder.package(root, root / 'release', 'https://example.azurewebsites.net', '2.0.0')
            with zipfile.ZipFile(root / 'release' / 'downloads' / 'copilot-ready-source.zip') as archive:
                names = archive.namelist()
                self.assertIn('go.mod', names)
                self.assertIn('website/App.jsx', names)
                self.assertIn('package-lock.json', names)
                self.assertFalse(any(name.startswith('node_modules/') for name in names))
                self.assertNotIn('personal-profile.json', names)
                self.assertNotIn('.git/config', names)
                self.assertFalse(any(name.startswith('build/') for name in names))


if __name__ == '__main__':
    unittest.main()
