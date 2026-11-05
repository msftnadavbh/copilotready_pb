package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExistingProfileKeepsPreferencesUnlessAsked(t *testing.T) {
	existing := []byte(`{
  // my choices
  "autoUpdate": false,
  "defaultPermissionMode": "auto",
  "sandbox": {"enabled": false, "auth": {"git": true, "gh": true}}
}`)
	kept, err := patchSettings(existing, false, false, "preserve", false)
	if err != nil {
		t.Fatal(err)
	}
	if changes := settingChanges(existing, kept); len(changes) != 0 {
		t.Fatalf("unrequested changes: %v", changes)
	}
	chosen, err := patchSettings(existing, false, false, "sandbox", true)
	if err != nil {
		t.Fatal(err)
	}
	// Windows also allows the local Git sign-in proxy; every change is listed for review.
	changes := settingChanges(existing, chosen)
	if !slices.Contains(changes, "sandbox.enabled: false → true") || slices.ContainsFunc(changes, func(c string) bool {
		return strings.HasPrefix(c, "autoUpdate") || strings.HasPrefix(c, "defaultPermissionMode") || strings.HasPrefix(c, "sandbox.auth")
	}) {
		t.Fatalf("explicit sandbox choice changed %v", changes)
	}
	fresh, err := patchSettings(nil, true, false, "manual", false)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(settingChanges(nil, fresh), "\n")
	for _, want := range []string{`autoUpdate: not set → true`, `defaultPermissionMode: not set → "manual"`, `sandbox.enabled: not set → false`} {
		if !strings.Contains(got, want) {
			t.Fatalf("new profile changes %q miss %q", got, want)
		}
	}
	if current := currentSettings(writeSettings(t, existing)); current["gitAuth"] != true {
		t.Fatalf("current=%v", current)
	}
}

func writeSettings(t *testing.T, data []byte) string {
	t.Helper()
	profile := t.TempDir()
	if err := os.WriteFile(filepath.Join(profile, "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestUndoWaitsForConversationImport(t *testing.T) {
	a := &app{imports: &importProgress{Running: true}}
	for _, action := range []string{"undo", "uninstall"} {
		r := httptest.NewRequest("POST", "/api/maintenance", strings.NewReader(`{"action":"`+action+`","confirm":true,"id":""}`))
		if err := a.maintenance(httptest.NewRecorder(), r); err == nil || !strings.Contains(err.Error(), "still importing") {
			t.Fatalf("%s during an import: %v", action, err)
		}
	}
}
