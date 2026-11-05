package installer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"copilotready/internal/core"
	"github.com/tailscale/hujson"
)

type hookOwnership struct {
	Profile string `json:"profile"`
	SHA256  string `json:"sha256"`
}

func (m *Manager) readHookOwnership() (map[string]hookOwnership, string, error) {
	path := filepath.Join(m.Root, "hook-ownership.json")
	if err := safePath(path, []string{m.Root}); err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]hookOwnership{}, "absent", nil
	}
	if err != nil {
		return nil, "", err
	}
	var owned map[string]hookOwnership
	if err = json.Unmarshal(data, &owned); err != nil || owned == nil {
		return nil, "", errors.New("hook ownership record needs review")
	}
	return owned, hash(data), nil
}

// PrepareHookOwnership joins the wrapper's activation transaction; it writes nothing.
func (m *Manager) PrepareHookOwnership(profile string, wrappers []core.Change) (core.Change, error) {
	owned, before, err := m.readHookOwnership()
	if err != nil {
		return core.Change{}, err
	}
	for _, wrapper := range wrappers {
		if err = safePath(wrapper.Path, []string{filepath.Join(profile, "hooks")}); err != nil {
			return core.Change{}, err
		}
		if filepath.Dir(wrapper.Path) != filepath.Join(profile, "hooks") || filepath.Ext(wrapper.Path) != ".json" {
			return core.Change{}, errors.New("hook ownership accepts only active profile hook files")
		}
		owned[wrapper.Path] = hookOwnership{Profile: profile, SHA256: hash(wrapper.Content)}
	}
	data, err := json.MarshalIndent(owned, "", "  ")
	if err != nil {
		return core.Change{}, err
	}
	return core.Change{Path: filepath.Join(m.Root, "hook-ownership.json"), Content: append(data, '\n'), BeforeSHA256: before, Mode: 0600, Source: "Copilot Ready hook ownership"}, nil
}

// Called before deleting the helper: generated hooks cannot outlive their executable.
func (m *Manager) removeHookWrappers() error {
	owned, _, err := m.readHookOwnership()
	if err != nil {
		return err
	}
	profiles := map[string]bool{filepath.Join(m.Root, "profiles", "default"): true, filepath.Join(m.Home, ".copilot"): true}
	if profile := os.Getenv("COPILOT_HOME"); filepath.IsAbs(profile) {
		profiles[profile] = true
	}
	removable := map[string]string{}
	preserved := []string{}
	for path, record := range owned {
		if filepath.Dir(path) != filepath.Join(record.Profile, "hooks") || filepath.Ext(path) != ".json" {
			return errors.New("unexpected hook ownership target; helper preserved for review")
		}
		if err = safePath(path, []string{filepath.Join(record.Profile, "hooks")}); err != nil {
			return err
		}
		profiles[record.Profile] = true
		actual, e := Snapshot(path)
		if e != nil {
			return e
		}
		if actual == "absent" {
			continue
		}
		if actual == record.SHA256 {
			removable[path] = actual
		} else {
			preserved = append(preserved, path)
		}
	}
	launchPath := filepath.Join(m.Root, "launch.json")
	if err = safePath(launchPath, []string{m.Root}); err != nil {
		return err
	}
	if data, e := os.ReadFile(launchPath); e == nil {
		var opts LaunchOptions
		if json.Unmarshal(data, &opts) != nil || !filepath.IsAbs(opts.Profile) {
			return errors.New("saved profile needs review before removing hook dependencies")
		}
		profiles[opts.Profile] = true
	} else if !os.IsNotExist(e) {
		return e
	}
	for profile := range profiles {
		paths := []string{filepath.Join(profile, "settings.json")}
		dir := filepath.Join(profile, "hooks")
		if err = safePath(dir, []string{profile}); err != nil {
			return err
		}
		entries, e := os.ReadDir(dir)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
				paths = append(paths, filepath.Join(dir, entry.Name()))
			}
		}
		for _, path := range paths {
			if _, tracked := owned[path]; tracked {
				continue
			}
			if err = safePath(path, []string{profile}); err != nil {
				return err
			}
			data, e := os.ReadFile(path)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			if m.hooksReferenceHelper(data) {
				return fmt.Errorf("untracked hook references the managed helper; review %s before uninstalling", path)
			}
		}
	}
	for path, expected := range removable {
		actual, e := Snapshot(path)
		if e != nil {
			return e
		}
		if actual != expected {
			preserved = append(preserved, path)
			continue
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	if len(preserved) > 0 {
		return fmt.Errorf("edited hook wrappers and their helper were preserved; review before uninstalling: %s", strings.Join(preserved, ", "))
	}
	return nil
}

func (m *Manager) hooksReferenceHelper(data []byte) bool {
	helper := filepath.Join(m.Root, "bin", "copilot-ready")
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	normalize := func(s string) string {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "''", "'"), "'\"'\"'", "'")
		s = strings.ReplaceAll(s, "\\", "/")
		if runtime.GOOS == "windows" {
			s = strings.ToLower(s)
		}
		return s
	}
	target := normalize(helper)
	standard, err := hujson.Standardize(data)
	var config map[string]any
	if err != nil || json.Unmarshal(standard, &config) != nil {
		return strings.Contains(normalize(string(data)), target)
	}
	var references func(any) bool
	references = func(v any) bool {
		switch value := v.(type) {
		case string:
			return strings.Contains(normalize(value), target)
		case []any:
			for _, child := range value {
				if references(child) {
					return true
				}
			}
		case map[string]any:
			for _, child := range value {
				if references(child) {
					return true
				}
			}
		}
		return false
	}
	return references(config["hooks"])
}
