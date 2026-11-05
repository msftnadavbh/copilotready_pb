package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"copilotready/internal/core"
)

func TestUninstallRemovesOnlyUneditedRegisteredHooks(t *testing.T) {
	t.Setenv("COPILOT_HOME", "")
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprintf("edited=%t", edited), func(t *testing.T) {
			home := t.TempDir()
			m := New(filepath.Join(home, "kit"), home)
			source := filepath.Join(home, "source")
			if err := os.WriteFile(source, []byte("helper"), 0700); err != nil {
				t.Fatal(err)
			}
			profile := filepath.Join(home, "profile")
			changes, err := m.PrepareLaunchers(source, LaunchOptions{Profile: profile, Project: home, Sandbox: "manual"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.Apply(changes, m.LauncherRoots()); err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(m.Root, "bin", "copilot-ready")
			if runtime.GOOS == "windows" {
				helper += ".exe"
			}
			data, _ := json.Marshal(map[string]any{"version": 1, "hooks": map[string]any{"SessionStart": []any{map[string]string{"type": "command", "command": helper + " --hook-adapter fixture"}}}})
			wrapper := core.Change{Path: filepath.Join(profile, "hooks", "copilot-ready-test.json"), Content: data, BeforeSHA256: "absent", Mode: 0600}
			ownership, err := m.PrepareHookOwnership(profile, []core.Change{wrapper})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(ownership.Path); !os.IsNotExist(err) {
				t.Fatalf("preparing ownership wrote a file: %v", err)
			}
			if _, err = m.Apply([]core.Change{wrapper, ownership}, []string{m.Root, profile}); err != nil {
				t.Fatal(err)
			}
			if edited {
				if err = os.WriteFile(wrapper.Path, append(data, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = m.Uninstall()
			if edited {
				if err == nil || !strings.Contains(err.Error(), "edited hook") {
					t.Fatalf("edited hook did not require review: %v", err)
				}
				for _, path := range []string{helper, wrapper.Path} {
					if _, err = os.Stat(path); err != nil {
						t.Fatalf("needed file removed: %s: %v", path, err)
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{helper, wrapper.Path} {
					if _, err = os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("owned file remains: %s: %v", path, err)
					}
				}
			}
		})
	}
}

func TestUninstallPreservesHelperForLegacyHook(t *testing.T) {
	for _, location := range []string{"saved", "managed-default", "standard", "environment"} {
		t.Run(location, func(t *testing.T) { testUninstallLegacyHook(t, location) })
	}
}

func testUninstallLegacyHook(t *testing.T, location string) {
	t.Helper()
	t.Setenv("COPILOT_HOME", "")
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	source := filepath.Join(home, "source")
	if err := os.WriteFile(source, []byte("helper"), 0700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(home, "profile")
	changes, err := m.PrepareLaunchers(source, LaunchOptions{Profile: profile, Project: home, Sandbox: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Apply(changes, m.LauncherRoots()); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(m.Root, "bin", "copilot-ready")
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	data, _ := json.Marshal(map[string]any{"version": 1, "hooks": map[string]any{"SessionStart": []any{map[string]string{"type": "command", "command": helper + " --hook-adapter fixture"}}}})
	hookProfile := profile
	switch location {
	case "managed-default":
		hookProfile = filepath.Join(m.Root, "profiles", "default")
	case "standard":
		hookProfile = filepath.Join(m.Home, ".copilot")
	case "environment":
		hookProfile = filepath.Join(home, "custom")
		t.Setenv("COPILOT_HOME", hookProfile)
	}
	hook := filepath.Join(hookProfile, "hooks", "copilot-ready-legacy.json")
	if err = atomicWrite(hook, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.Uninstall(); err == nil {
		t.Fatal("uninstall orphaned an untracked hook")
	}
	if _, err = os.Stat(helper); err != nil {
		t.Fatalf("needed helper was removed: %v", err)
	}
	if _, err = os.Stat(hook); err != nil {
		t.Fatalf("legacy hook was removed: %v", err)
	}
}

func TestLauncherPreviewDoesNotCreateManagedRoot(t *testing.T) {
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	helper := filepath.Join(home, "helper")
	os.WriteFile(helper, []byte("binary"), 0700)
	opts := LaunchOptions{Profile: filepath.Join(home, "profile"), Project: home, Sandbox: "manual"}
	changes, err := m.PrepareLaunchers(helper, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) < 3 {
		t.Fatal("launcher files missing")
	}
	if _, err = os.Stat(m.Root); !os.IsNotExist(err) {
		t.Fatal("preview created managed folder")
	}
}

func TestMacPathRemovalPreservesOtherShellEdits(t *testing.T) {
	home := t.TempDir()
	m := New(filepath.Join(home, "kit"), home)
	if err := m.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", "/bin/zsh")
	profile := filepath.Join(home, ".zprofile")
	os.WriteFile(profile, []byte("export EXISTING=yes\n"), 0600)
	if err := m.addMacPath(); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(profile, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("export LATER=yes\n")
	f.Close()
	if err := m.removePathRegistration(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(profile)
	if string(got) != "export EXISTING=yes\nexport LATER=yes\n" {
		t.Fatalf("shell edit not preserved: %s", got)
	}
	if strings.Contains(string(got), "Copilot Ready") {
		t.Fatal("managed block remains")
	}
}
