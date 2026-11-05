package main

import (
	"encoding/json"
	"fmt"
	"github.com/tailscale/hujson"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// patchSettings preserves comments and unrelated values instead of reserializing a profile.
// An existing profile keeps its preferences unless recommended settings are chosen, and a
// "preserve" sandbox choice leaves sandbox and Git sign-in as they are.
func patchSettings(input []byte, newProfile, baseline bool, sandbox string, gitAuth bool) ([]byte, error) {
	if len(input) == 0 {
		input = []byte("{}")
	}
	v, err := hujson.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("existing settings need repair before migration: %w", err)
	}
	clean := v.Clone()
	clean.Standardize()
	var obj map[string]any
	if err = json.Unmarshal(clean.Pack(), &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("settings must be an object")
	}
	updates := map[string]any{}
	if newProfile || baseline {
		updates["autoUpdate"] = true
		updates["defaultPermissionMode"] = "manual"
	}
	if baseline {
		updates["defaultMode"] = "interactive"
		updates["memory"] = false
		updates["model"] = "auto"
		updates["autoTier"] = "balance"
		updates["continueOnAutoMode"] = false
	}
	for k, x := range updates {
		b, _ := json.Marshal([]map[string]any{{"op": "add", "path": "/" + k, "value": x}})
		if err = v.Patch(b); err != nil {
			return nil, err
		}
	}
	if sandbox != "preserve" {
		if sandbox != "sandbox" && sandbox != "manual" {
			return nil, fmt.Errorf("unknown sandbox choice")
		}
		if _, ok := obj["sandbox"].(map[string]any); !ok {
			if _, exists := obj["sandbox"]; exists {
				return nil, fmt.Errorf("sandbox settings must be an object")
			}
			if err = v.Patch([]byte(`[{"op":"add","path":"/sandbox","value":{}}]`)); err != nil {
				return nil, err
			}
		}
		if err = v.Patch([]byte(fmt.Sprintf(`[{"op":"add","path":"/sandbox/enabled","value":%t}]`, sandbox == "sandbox"))); err != nil {
			return nil, err
		}
		if sandbox == "sandbox" {
			// Authentication is a separate user choice, including the local proxy requirement.
			clean = v.Clone()
			clean.Standardize()
			_ = json.Unmarshal(clean.Pack(), &obj)
			sb := obj["sandbox"].(map[string]any)
			if _, ok := sb["auth"].(map[string]any); !ok {
				if _, exists := sb["auth"]; exists {
					return nil, fmt.Errorf("sandbox auth must be an object")
				}
				_ = v.Patch([]byte(`[{"op":"add","path":"/sandbox/auth","value":{}}]`))
			}
			b, _ := json.Marshal([]map[string]any{{"op": "add", "path": "/sandbox/auth/git", "value": gitAuth}, {"op": "add", "path": "/sandbox/auth/gh", "value": gitAuth}})
			if err = v.Patch(b); err != nil {
				return nil, err
			}
			if gitAuth && runtime.GOOS == "windows" {
				clean = v.Clone()
				clean.Standardize()
				_ = json.Unmarshal(clean.Pack(), &obj)
				current := obj["sandbox"].(map[string]any)
				for _, key := range []string{"userPolicy", "network"} {
					child, ok := current[key].(map[string]any)
					if !ok {
						if _, exists := current[key]; exists {
							return nil, fmt.Errorf("sandbox %s must be an object", key)
						}
						path := "/sandbox/userPolicy"
						if key == "network" {
							path += "/network"
						}
						patch, _ := json.Marshal([]map[string]any{{"op": "add", "path": path, "value": map[string]any{}}})
						if err = v.Patch(patch); err != nil {
							return nil, err
						}
						child = map[string]any{}
					}
					current = child
				}
				if err = v.Patch([]byte(`[{"op":"add","path":"/sandbox/userPolicy/network/allowLocalNetwork","value":true}]`)); err != nil {
					return nil, err
				}
			}
		}
	}
	v.Format()
	return v.Pack(), nil
}

// currentSettings reports the existing profile's Git sign-in choice, so the setup form starts
// from it instead of silently turning it off.
func currentSettings(profile string) map[string]any {
	var settings struct {
		Sandbox struct {
			Auth struct {
				Git bool `json:"git"`
			} `json:"auth"`
		} `json:"sandbox"`
	}
	if b, err := os.ReadFile(filepath.Join(profile, "settings.json")); err == nil {
		if v, err := hujson.Parse(b); err == nil {
			v.Standardize()
			_ = json.Unmarshal(v.Pack(), &settings)
		}
	}
	return map[string]any{"gitAuth": settings.Sandbox.Auth.Git}
}

// settingChanges lists every setting whose value differs as "name: old → new", so the review
// shows exactly what will change in the profile.
func settingChanges(before, after []byte) []string {
	flatten := func(b []byte) map[string]string {
		out := map[string]string{}
		v, err := hujson.Parse(b)
		if err != nil {
			return out
		}
		v.Standardize()
		var root any
		_ = json.Unmarshal(v.Pack(), &root)
		var walk func(prefix string, x any)
		walk = func(prefix string, x any) {
			if obj, ok := x.(map[string]any); ok && len(obj) > 0 {
				for k, y := range obj {
					walk(strings.TrimPrefix(prefix+"."+k, "."), y)
				}
				return
			}
			if prefix != "" {
				s, _ := json.Marshal(x)
				out[prefix] = string(s)
			}
		}
		walk("", root)
		return out
	}
	old, next := flatten(before), flatten(after)
	var changes []string
	for k, value := range next {
		if previous, ok := old[k]; !ok {
			changes = append(changes, k+": not set → "+value)
		} else if previous != value {
			changes = append(changes, k+": "+previous+" → "+value)
		}
	}
	for k, previous := range old {
		if _, ok := next[k]; !ok {
			changes = append(changes, k+": "+previous+" → removed")
		}
	}
	sort.Strings(changes)
	return changes
}

func redactCommandOutput(s string) string {
	// CLI diagnostics can contain credentials in commands or environment values.
	// Return only a bounded error category; users view detailed native diagnostics in their terminal.
	s = strings.TrimSpace(s)
	if s == "" {
		return "The native check returned no diagnostic output."
	}
	return "The native check reported a problem. Open Copilot to review its local settings diagnostics."
}
