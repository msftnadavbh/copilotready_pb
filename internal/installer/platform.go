package installer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"copilotready/internal/core"
)

type PrerequisitesInfo struct {
	Supported               bool   `json:"supported"`
	OS                      string `json:"os"`
	Arch                    string `json:"arch"`
	PowerShell              string `json:"powerShell"`
	SandboxHint             string `json:"sandboxHint"`
	NeedsPowerShell         bool   `json:"needsPowerShell"`
	SandboxEligible         bool   `json:"sandboxEligible"`
	SandboxEligibilityKnown bool   `json:"sandboxEligibilityKnown"`
}

func Prerequisites() PrerequisitesInfo {
	p := PrerequisitesInfo{OS: runtime.GOOS, Arch: runtime.GOARCH}
	_, err := PlatformAsset()
	p.Supported = err == nil
	switch runtime.GOOS {
	case "windows":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, versionErr := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", "[Environment]::OSVersion.Version.Build").Output()
		cancel()
		build, parseErr := strconv.Atoi(strings.TrimSpace(string(out)))
		if versionErr == nil && parseErr == nil {
			p.SandboxEligibilityKnown = true
			p.SandboxEligible = build >= 26200
		}
		candidates := []string{"pwsh.exe", filepath.Join(os.Getenv("ProgramFiles"), "PowerShell", "7", "pwsh.exe")}
		for _, candidate := range candidates {
			path, err := exec.LookPath(candidate)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			out, err := exec.CommandContext(ctx, path, "-NoProfile", "-Command", "$PSVersionTable.PSVersion.Major").Output()
			cancel()
			major, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			if err == nil && major >= 6 {
				p.PowerShell = path
				break
			}
		}
		p.NeedsPowerShell = p.PowerShell == ""
		p.SandboxHint = "Requires a supported Windows 11 build and BaseContainer. Eligibility is not proof of enforcement; check Copilot's session sandbox status."
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, versionErr := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
		cancel()
		major, parseErr := strconv.Atoi(strings.Split(strings.TrimSpace(string(out)), ".")[0])
		if versionErr == nil && parseErr == nil {
			p.SandboxEligibilityKnown = true
			p.SandboxEligible = major >= 15
		}
		p.SandboxHint = "Requires a supported macOS host and sandbox backend. Confirm actual enforcement in Copilot before relying on it."
	default:
		p.SandboxHint = "Linux and WSL are not supported by this installer release."
	}
	return p
}
func InstallPowerShell(ctx context.Context) error {
	if runtime.GOOS != "windows" {
		return errors.New("PowerShell installation is Windows-only")
	}
	if !Prerequisites().NeedsPowerShell {
		return nil
	}
	winget, err := exec.LookPath("winget.exe")
	if err != nil {
		return errors.New("WinGet is unavailable; use the Microsoft PowerShell installer, then reopen setup")
	}
	cmd := exec.CommandContext(ctx, winget, "install", "--id", "Microsoft.PowerShell", "--exact", "--source", "winget", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("PowerShell installation did not finish: %w", err)
	}
	if Prerequisites().NeedsPowerShell {
		return errors.New("PowerShell is not yet discoverable; reopen setup after installation or restart")
	}
	return nil
}

func OpenBrowser(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		return errors.New("setup browser URL must be local loopback")
	}
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", raw)
		return cmd.Start()
	case "darwin":
		return exec.Command("/usr/bin/open", raw).Start()
	default:
		return errors.New("browser launch unsupported on this platform")
	}
}
func PickFolder() (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell.exe", "-NoProfile", "-STA", "-Command", `Add-Type -AssemblyName System.Windows.Forms; $d=New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description='Choose the project to open in Copilot'; $d.ShowNewFolderButton=$false; if($d.ShowDialog() -eq 'OK'){[Console]::OutputEncoding=[Text.Encoding]::UTF8; [Console]::Write($d.SelectedPath)}`)
	case "darwin":
		cmd = exec.Command("/usr/bin/osascript", "-e", `try`, "-e", `POSIX path of (choose folder with prompt "Choose the project to open in Copilot")`, "-e", `on error number -128`, "-e", `return ""`, "-e", `end try`)
	default:
		return "", errors.New("folder picker unavailable")
	}
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("folder picker did not finish")
	}
	path := strings.TrimSpace(strings.TrimPrefix(string(out), "\ufeff"))
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("folder picker returned a relative path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("selected folder is unavailable")
	}
	return filepath.Clean(path), nil
}

func psValue(s string) string {
	return "[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString([]byte(s)) + "'))"
}
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

const windowsPathBroadcast = `; try { Add-Type -Namespace CopilotReady -Name EnvironmentBroadcast -MemberDefinition '[System.Runtime.InteropServices.DllImport("user32.dll", CharSet=System.Runtime.InteropServices.CharSet.Unicode)] public static extern System.IntPtr SendMessageTimeout(System.IntPtr window, uint message, System.UIntPtr wParam, string lParam, uint flags, uint timeout, out System.UIntPtr result);'; $sent=[UIntPtr]::Zero; [CopilotReady.EnvironmentBroadcast]::SendMessageTimeout([IntPtr]0xffff,0x1a,[UIntPtr]::Zero,'Environment',2,5000,[ref]$sent) | Out-Null } catch {}`

func (m *Manager) visibleDirs() []string {
	if runtime.GOOS == "windows" {
		return []string{filepath.Join(m.Home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Copilot Ready")}
	}
	return []string{filepath.Join(m.Home, "Applications")}
}
func (m *Manager) LauncherRoots() []string { return append([]string{m.Root}, m.visibleDirs()...) }
func (m *Manager) PrepareLaunchers(helperPath string, opts LaunchOptions) ([]core.Change, error) {
	if err := safePath(m.Root, []string{m.Root}); err != nil {
		return nil, err
	}
	if _, err := os.Stat(m.Root); err == nil {
		if err = m.EnsureRoot(); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if !filepath.IsAbs(opts.Profile) || !filepath.IsAbs(opts.Project) {
		return nil, errors.New("profile and project must be absolute paths")
	}
	if !validSandbox(opts.Sandbox) {
		return nil, errors.New("choose sandbox or manual mode")
	}
	helper, err := os.ReadFile(helperPath)
	if err != nil {
		return nil, err
	}
	name := "copilot-ready"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(m.Root, "bin", name)
	launchData, _ := json.MarshalIndent(opts, "", "  ")
	files := map[string][]byte{dest: helper, filepath.Join(m.Root, "launch.json"): append(launchData, '\n')}
	modes := map[string]uint32{dest: 0700}
	dir := m.visibleDirs()[0]
	if runtime.GOOS == "windows" {
		for _, entry := range []struct{ Name, Flag string }{{"Launch Copilot.cmd", "--launch"}, {"Copilot Ready Setup.cmd", "--choose-project"}} {
			script := "@echo off\r\npowershell.exe -NoProfile -Command \"$p=" + psValue(dest) + "; $r=" + psValue(m.Root) + "; & $p --root $r " + entry.Flag + "\"\r\nif errorlevel 1 pause\r\n"
			files[filepath.Join(dir, entry.Name)] = []byte(script)
		}
		files[filepath.Join(m.Root, "command", "copilot.cmd")] = []byte("@echo off\r\nsetlocal DisableDelayedExpansion\r\n\"%~dp0..\\bin\\copilot-ready.exe\" --run-cli \"%~dp0..\" \"" + strings.ReplaceAll(m.Home, "%", "%%") + "\" -- %*\r\nexit /b %errorlevel%\r\n")
	} else if runtime.GOOS == "darwin" {
		command := filepath.Join(m.Root, "command", "copilot")
		files[command] = []byte("#!/bin/sh\nexec " + shQuote(dest) + " --run-cli " + shQuote(m.Root) + " " + shQuote(m.Home) + " -- \"$@\"\n")
		modes[command] = 0700
		for _, entry := range []struct{ Name, Flag string }{{"Copilot Ready.command", "--launch"}, {"Copilot Ready Setup.command", "--choose-project"}} {
			path := filepath.Join(dir, entry.Name)
			files[path] = []byte("#!/bin/bash\nexec " + shQuote(dest) + " --root " + shQuote(m.Root) + " " + entry.Flag + "\n")
			modes[path] = 0700
		}
	} else {
		return nil, errors.New("launchers support Windows/macOS only")
	}
	ownedPath := filepath.Join(m.Root, "owned-files.json")
	if err = safePath(ownedPath, []string{m.Root}); err != nil {
		return nil, err
	}
	owned := map[string]string{}
	if b, e := os.ReadFile(ownedPath); e == nil {
		if e = json.Unmarshal(b, &owned); e != nil {
			return nil, errors.New("owned-file record is invalid")
		}
	}
	changes := []core.Change{}
	for path, content := range files {
		if err = safePath(path, m.LauncherRoots()); err != nil {
			return nil, err
		}
		before, e := Snapshot(path)
		if e != nil {
			return nil, e
		}
		if before != "absent" && before != hash(content) && owned[path] != before {
			return nil, fmt.Errorf("existing launcher was edited or is not owned: %s", path)
		}
		mode := modes[path]
		if mode == 0 {
			mode = 0600
		}
		if before != hash(content) {
			changes = append(changes, core.Change{Path: path, Content: content, BeforeSHA256: before, Mode: mode, Source: "Copilot Ready launcher"})
		}
		owned[path] = hash(content)
	}
	data, _ := json.MarshalIndent(owned, "", "  ")
	before, err := Snapshot(ownedPath)
	if err != nil {
		return nil, err
	}
	changes = append(changes, core.Change{Path: ownedPath, Content: append(data, '\n'), BeforeSHA256: before, Mode: 0600, Source: "Copilot Ready ownership"})
	return changes, nil
}

// RunCLI keeps normal Copilot commands in the caller's terminal and working directory.
func (m *Manager) RunCLI(args []string) error {
	rt, err := m.VerifyRuntime()
	if err != nil {
		return err
	}
	path := filepath.Join(m.Root, "launch.json")
	if err = safePath(path, []string{m.Root}); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var opts LaunchOptions
	if json.Unmarshal(data, &opts) != nil || !filepath.IsAbs(opts.Profile) || !validSandbox(opts.Sandbox) {
		return errors.New("saved Copilot profile needs review in setup")
	}
	flags := []string{"--sync-session=off"}
	if opts.Sandbox == "sandbox" {
		flags = append(flags, "--sandbox")
	}
	cmd := exec.Command(rt.Path, append(flags, args...)...)
	cmd.Env = m.Environment(opts.Profile)
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh.exe"); err != nil {
			pwsh := filepath.Join(os.Getenv("ProgramFiles"), "PowerShell", "7", "pwsh.exe")
			if info, err := os.Stat(pwsh); err == nil && !info.IsDir() {
				cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(pwsh)+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func validSandbox(choice string) bool {
	return choice == "manual" || choice == "sandbox" || choice == "preserve"
}

// launchNormal opens a new terminal in the project and runs copilot exactly as typing it would,
// with the PATH new terminals get, so Open Copilot and the copilot command never differ.
func launchNormal(project string) error {
	switch runtime.GOOS {
	case "windows":
		// The new window inherits this baseline, then its PowerShell profile can adjust it as usual.
		script := `$env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User'); Start-Process -FilePath powershell.exe -WorkingDirectory $env:COPILOT_READY_PROJECT -ArgumentList '-NoExit','-Command','copilot'`
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", script)
		cmd.Env = append(os.Environ(), "COPILOT_READY_PROJECT="+project)
		return cmd.Run()
	case "darwin":
		script := "#!/bin/bash\ncd " + shQuote(project) + " || exit 1\n\"${SHELL:-/bin/zsh}\" -l -i -c copilot\nstatus=$?\nif [ \"$status\" -ne 0 ]; then read -r -p 'Copilot stopped. Press Return to close.'; fi\nexit \"$status\"\n"
		f, err := os.CreateTemp("", "copilot-ready-*.command")
		if err != nil {
			return err
		}
		_, err = f.WriteString(script)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Chmod(f.Name(), 0700)
		}
		if err != nil {
			return err
		}
		return exec.Command("/usr/bin/open", "-a", "Terminal", f.Name()).Start()
	}
	return errors.New("unsupported launch platform")
}

func (m *Manager) Launch(opts LaunchOptions) error {
	if !validSandbox(opts.Sandbox) {
		return errors.New("invalid launch mode")
	}
	pre := Prerequisites()
	if opts.Sandbox == "sandbox" && pre.SandboxEligibilityKnown && !pre.SandboxEligible {
		return errors.New("this operating system is below the documented sandbox prerequisites; return to setup and choose manual mode, or update the operating system")
	}
	if !filepath.IsAbs(opts.Profile) || !filepath.IsAbs(opts.Project) {
		return errors.New("profile/project must be absolute")
	}
	for _, path := range []string{opts.Profile, opts.Project} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("launch folder unavailable: %s", path)
		}
	}
	if opts.Normal {
		return launchNormal(opts.Project)
	}
	rt, err := m.VerifyRuntime()
	if err != nil {
		return err
	}
	args := []string{"--sync-session=off"}
	if opts.Sandbox == "sandbox" {
		args = append(args, "--sandbox")
	}
	env := m.Environment(opts.Profile)
	if runtime.GOOS == "windows" {
		p := pre
		if p.NeedsPowerShell {
			return errors.New("PowerShell 6+ is required; install PowerShell 7 from setup")
		}
		env = append(env, "PATH="+filepath.Dir(p.PowerShell)+string(os.PathListSeparator)+os.Getenv("PATH"))
		env = append(env, "COPILOT_READY_RUNTIME="+rt.Path, "COPILOT_READY_PROJECT="+opts.Project)
		quoted := []string{}
		for _, a := range args {
			quoted = append(quoted, "'"+a+"'")
		}
		script := `Start-Process -FilePath $env:COPILOT_READY_RUNTIME -WorkingDirectory $env:COPILOT_READY_PROJECT -ArgumentList @(` + strings.Join(quoted, ",") + `)`
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", script)
		cmd.Env = env
		return cmd.Run()
	}
	if runtime.GOOS == "darwin" {
		script := "#!/bin/bash\nset -u\nunset COPILOT_ALLOW_ALL COPILOT_ASSISTED_APPROVAL COPILOT_MODEL COPILOT_CLI_DIST_DIR\n"
		for _, pair := range []string{"COPILOT_HOME=" + opts.Profile, "COPILOT_CACHE_HOME=" + filepath.Join(m.Root, "cache"), "COPILOT_PKG_CACHE_HOME=" + filepath.Join(m.Root, "cache")} {
			key, value, _ := strings.Cut(pair, "=")
			script += "export " + key + "=" + shQuote(value) + "\n"
		}
		script += "cd " + shQuote(opts.Project) + " || exit 1\n" + shQuote(rt.Path)
		for _, a := range args {
			script += " " + shQuote(a)
		}
		script += "\nstatus=$?\nif [ \"$status\" -ne 0 ]; then read -r -p 'Copilot stopped. Press Return to close.'; fi\nexit \"$status\"\n"
		path := filepath.Join(m.Root, "launch-session.command")
		if err = safePath(path, []string{m.Root}); err != nil {
			return err
		}
		if err = atomicWrite(path, []byte(script), 0700); err != nil {
			return err
		}
		return exec.Command("/usr/bin/open", "-a", "Terminal", path).Start()
	}
	return errors.New("unsupported launch platform")
}

func (m *Manager) AddToPath() error {
	bin := filepath.Join(m.Root, "command")
	if err := m.EnsureRoot(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		release, err := m.Acquire()
		if err != nil {
			return err
		}
		defer release()
		if err = m.removePathRegistration(); err != nil {
			return err
		}
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", `$bin=$env:COPILOT_READY_BIN; $old=[string][Environment]::GetEnvironmentVariable('Path','User'); if(($old -split ';') -notcontains $bin){[Environment]::SetEnvironmentVariable('Path',($old.TrimEnd(';')+';'+$bin),'User'); 'added'}`+windowsPathBroadcast)
		cmd.Env = append(os.Environ(), "COPILOT_READY_BIN="+bin)
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) == "added" {
			return writeJSON(filepath.Join(m.Root, "path-registration.json"), pathRegistration{Kind: "windows", Bin: bin})
		}
		return nil
	}
	if runtime.GOOS == "darwin" {
		release, err := m.Acquire()
		if err != nil {
			return err
		}
		err = m.removePathRegistration()
		release()
		if err != nil {
			return err
		}
		return m.addMacPath()
	}
	return errors.New("command PATH setup supports Windows and macOS only")
}

type pathRegistration struct {
	Kind     string `json:"kind"`
	Bin      string `json:"bin"`
	File     string `json:"file,omitempty"`
	Block    string `json:"block,omitempty"`
	Shim     string `json:"shim,omitempty"`
	ShimHash string `json:"shimHash,omitempty"`
}

func (m *Manager) addMacPath() error {
	bin := filepath.Join(m.Root, "command")
	profile := filepath.Join(m.Home, ".zprofile")
	if filepath.Base(os.Getenv("SHELL")) == "bash" {
		profile = filepath.Join(m.Home, ".bash_profile")
	}
	marker := filepath.Join(m.Root, "path-registration.json")
	if b, e := os.ReadFile(marker); e == nil {
		var record pathRegistration
		if json.Unmarshal(b, &record) == nil && record.Kind == "darwin" {
			return nil
		}
		return errors.New("PATH ownership record needs review")
	}
	for _, p := range []string{profile, marker} {
		if err := safePath(p, []string{m.Home, m.Root}); err != nil {
			return err
		}
	}
	profileBefore, err := Snapshot(profile)
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(profile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(existing), "# >>> Copilot Ready PATH >>>") {
		return errors.New("an existing Copilot Ready PATH block needs ownership review")
	}
	block := "\n# >>> Copilot Ready PATH >>>\nexport PATH=\"$PATH\":" + shQuote(bin) + "\n# <<< Copilot Ready PATH <<<\n"
	record := pathRegistration{Kind: "darwin", Bin: bin, File: profile, Block: block}
	data, _ := json.MarshalIndent(record, "", "  ")
	_, err = m.Apply([]core.Change{{Path: profile, Content: append(existing, []byte(block)...), BeforeSHA256: profileBefore, Mode: 0600, Source: "Copilot command PATH"}, {Path: marker, Content: append(data, '\n'), BeforeSHA256: "absent", Mode: 0600, Source: "PATH ownership"}}, []string{m.Root, m.Home})
	return err
}

// Called while Uninstall holds the operation lock. Remove our exact addition only.
func (m *Manager) removePathRegistration() error {
	marker := filepath.Join(m.Root, "path-registration.json")
	if err := safePath(marker, []string{m.Root}); err != nil {
		return err
	}
	data, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var record pathRegistration
	if err = json.Unmarshal(data, &record); err != nil {
		return err
	}
	if record.Kind == "windows" && runtime.GOOS == "windows" {
		if record.Bin != filepath.Join(m.Root, "bin") && record.Bin != filepath.Join(m.Root, "command") {
			return errors.New("unexpected PATH ownership target")
		}
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", `$bin=$env:COPILOT_READY_BIN; $old=[string][Environment]::GetEnvironmentVariable('Path','User'); $new=(($old -split ';') | Where-Object {$_ -ne $bin}) -join ';'; [Environment]::SetEnvironmentVariable('Path',$new,'User')`+windowsPathBroadcast)
		cmd.Env = append(os.Environ(), "COPILOT_READY_BIN="+record.Bin)
		if err = cmd.Run(); err != nil {
			return err
		}
	}
	if record.Kind == "darwin" {
		legacyShim := filepath.Join(m.Home, ".local", "bin", "copilot-ready")
		validTarget := (record.Bin == filepath.Join(m.Root, "command") && record.Shim == "") || (record.Bin == filepath.Dir(legacyShim) && record.Shim == legacyShim)
		if !validTarget || (record.File != filepath.Join(m.Home, ".zprofile") && record.File != filepath.Join(m.Home, ".bash_profile")) {
			return errors.New("unexpected shell PATH ownership target")
		}
		paths := []string{record.File}
		if record.Shim != "" {
			paths = append(paths, record.Shim)
		}
		for _, p := range paths {
			if err = safePath(p, []string{m.Home}); err != nil {
				return err
			}
		}
		content, e := os.ReadFile(record.File)
		if e == nil && strings.Contains(string(content), record.Block) {
			updated := strings.Replace(string(content), record.Block, "", 1)
			if err = atomicWrite(record.File, []byte(updated), 0600); err != nil {
				return err
			}
		} else if e != nil && !os.IsNotExist(e) {
			return e
		}
		if record.Shim != "" {
			actual, e := Snapshot(record.Shim)
			if e != nil {
				return e
			}
			if actual == record.ShimHash {
				if err = os.Remove(record.Shim); err != nil {
					return err
				}
			} else if actual != "absent" {
				return errors.New("edited command shim preserved; remove it manually if no longer wanted")
			}
		}
	}
	return os.Remove(marker)
}

func (m *Manager) Uninstall() error {
	release, err := m.Acquire()
	if err != nil {
		return err
	}
	defer release()
	if err = m.removeHookWrappers(); err != nil {
		return err
	}
	if err = m.removePathRegistration(); err != nil {
		return err
	}
	owned := map[string]string{}
	path := filepath.Join(m.Root, "owned-files.json")
	if err = safePath(path, []string{m.Root}); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &owned); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	preserved := []string{}
	for file, want := range owned {
		if filepath.Base(file) == "launch.json" {
			continue
		}
		if err = safePath(file, m.LauncherRoots()); err != nil {
			return err
		}
		actual, e := Snapshot(file)
		if e != nil {
			return e
		}
		if actual == "absent" {
			continue
		}
		if actual != want {
			preserved = append(preserved, file)
			continue
		}
		if err = os.Remove(file); err != nil {
			preserved = append(preserved, file)
		}
	}
	for _, name := range []string{"runtimes", "cache"} {
		target := filepath.Join(m.Root, name)
		if err = safePath(target, []string{m.Root}); err != nil {
			return err
		}
		if err = filepath.Walk(target, func(p string, info os.FileInfo, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if linked(info) {
				return errors.New("linked folder in managed data; remove manually after review")
			}
			return nil
		}); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err = os.RemoveAll(target); err != nil {
			return err
		}
	}
	os.Remove(filepath.Join(m.Root, "runtime-state.json"))
	if len(preserved) > 0 {
		return fmt.Errorf("runtime removed; edited or currently running launchers preserved: %s", strings.Join(preserved, ", "))
	}
	return nil
}
