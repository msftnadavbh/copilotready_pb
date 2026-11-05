package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"copilotready/internal/installer"
	"copilotready/internal/migration"
	"copilotready/internal/sessions"
)

// The import runs real `copilot sessions import` into a throwaway profile, so it needs a Copilot
// CLI with that command (1.0.85 or later) on PATH.
func TestConversationImportReportsProgressAndRecordsImports(t *testing.T) {
	copilot, err := exec.LookPath("copilot")
	if err != nil || exec.Command(copilot, "--no-auto-update", "sessions", "import", "--help").Run() != nil {
		t.Skip("Copilot CLI with sessions import is not on PATH")
	}
	home, root, work := t.TempDir(), t.TempDir(), t.TempDir()
	profile := filepath.Join(root, "profiles", "default")
	transcript := func(id, text string) sessions.Session {
		line, _ := json.Marshal(map[string]any{"type": "user", "uuid": "u1", "cwd": work, "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user", "content": text}})
		p := filepath.Join(home, ".claude", "projects", "p", id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, append(line, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		return sessions.Session{Source: "claude", ID: id, Title: text, Cwd: work, Path: p}
	}
	updated := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	first := transcript("11111111-1111-1111-1111-111111111111", "Fix the build")
	first.Updated = updated
	list := []sessions.Session{
		first,
		transcript("22222222-2222-2222-2222-222222222222", "<command-name>/clear</command-name>"),
		{Source: "codex", ID: "t1", Title: "Codex thread", Cwd: work},
	}
	a := &app{root: root, manager: installer.New(root, home), imports: &importProgress{Running: true, Total: len(list)}}
	o := migration.Options{SessionRecord: filepath.Join(root, "imported-sessions.json")}
	a.importConversations(copilot, profile, o, list, false, afterShowing{})
	p := a.imports
	if p.Running || p.Done != 3 || p.Imported != 1 || p.Skipped != 1 || p.Failed != 1 || !strings.Contains(p.Failures[0], "Codex is not installed") {
		t.Fatalf("progress=%+v", p)
	}
	if !sessions.OpenRecord(o.SessionRecord).Has(profile, "claude:11111111-1111-1111-1111-111111111111") {
		t.Fatal("imported conversation was not recorded")
	}
	// /resume order and --continue follow the session file's time, so it carries the original update.
	var record map[string]map[string]string
	data, _ := os.ReadFile(o.SessionRecord)
	_ = json.Unmarshal(data, &record)
	for _, id := range record {
		for _, copilotID := range id {
			if info, err := os.Stat(filepath.Join(profile, "session-state", copilotID, "events.jsonl")); err != nil || !info.ModTime().Equal(updated) {
				t.Fatalf("session time=%v err=%v", info.ModTime(), err)
			}
		}
	}
	states, _ := os.ReadDir(filepath.Join(profile, "session-state"))
	imported := 0
	for _, s := range states {
		if _, err := os.Stat(filepath.Join(profile, "session-state", s.Name(), "events.jsonl")); err == nil {
			imported++
		}
	}
	if imported != 1 {
		t.Fatalf("%d sessions in the profile", imported)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, "conversations-*")); len(leftovers) != 0 {
		t.Fatalf("scratch files left: %v", leftovers)
	}
}
