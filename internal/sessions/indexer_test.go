package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIndexerWithRealCopilot imports a conversation as long as an import gets, then indexes it with
// the Copilot CLI on PATH, offline against the stand-in model. Run it with the network cut off
// (for example `unshare -rn` on Linux) to show that nothing leaves the computer.
func TestIndexerWithRealCopilot(t *testing.T) {
	copilot, err := exec.LookPath("copilot")
	if err != nil || exec.Command(copilot, "--no-auto-update", "sessions", "import", "--help").Run() != nil {
		t.Skip("Copilot CLI with sessions import is not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	profile, repo, markers := t.TempDir(), t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/octo-org/demo.git"}, {"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	marker := func(name string) (string, string) {
		p := filepath.Join(markers, name)
		return "echo started >> '" + p + "'", "Add-Content -Path '" + p + "' -Value started"
	}
	mcpBash, mcpPowerShell := marker("mcp")
	hookBash, hookPowerShell := marker("hook")
	mcp := map[string]any{"mcpServers": map[string]any{"fixture": map[string]any{"type": "local", "command": "sh", "args": []string{"-c", mcpBash}, "tools": []string{"*"}}}}
	if filepath.Separator == '\\' {
		mcp = map[string]any{"mcpServers": map[string]any{"fixture": map[string]any{"type": "local", "command": "powershell.exe", "args": []string{"-NoProfile", "-Command", mcpPowerShell}, "tools": []string{"*"}}}}
	}
	hooks := map[string]any{"version": 1, "hooks": map[string]any{"sessionStart": []any{map[string]any{"type": "command", "bash": hookBash, "powershell": hookPowerShell}}}}
	for name, v := range map[string]any{"mcp-config.json": mcp, "settings.json": map[string]any{"model": "claude-sonnet-4.6"}, filepath.Join("hooks", "existing.json"): hooks} {
		b, _ := json.Marshal(v)
		put(t, filepath.Join(profile, name), string(b))
	}

	updated := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	c := Conversation{Session: Session{Source: "claude", ID: "real", Title: "Long history", Cwd: repo, Branch: "main", Origin: "https://github.com/octo-org/demo.git", Updated: updated}}
	for turn := range 120 {
		c.Events = append(c.Events, Event{Kind: "user", Text: fmt.Sprintf("ORIGINAL-%d %s", turn, strings.Repeat("please look at this ", 40))},
			Event{Kind: "call", CallID: fmt.Sprint(turn), Name: "Read", Args: map[string]any{"path": "main.go"}},
			Event{Kind: "result", CallID: fmt.Sprint(turn), Text: strings.Repeat("package main // ", 120)},
			Event{Kind: "assistant", Text: fmt.Sprintf("ANSWER-%d done", turn)})
	}
	env := append(os.Environ(), "COPILOT_HOME="+profile, "COPILOT_CACHE_HOME="+t.TempDir(), "GH_TOKEN=must-not-be-used")
	id, err := Importer{Copilot: copilot, Env: env, Profile: profile, Dir: t.TempDir()}.Import(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(profile, "session-state", id, "events.jsonl")
	before, _ := os.ReadFile(events)

	scratch := t.TempDir()
	ix := &Indexer{Copilot: copilot, Env: env, Profile: profile, Dir: scratch}
	if err := ix.Start(); err != nil {
		t.Fatal(err)
	}
	err = ix.Index(context.Background(), id, "Claude Code")
	ix.Close()
	if err != nil {
		t.Fatal(err)
	}

	indexed, err := IndexedSessions(filepath.Join(profile, "session-store.db"))
	if err != nil || !indexed[id] {
		t.Fatalf("not in Copilot's index: %v %v", indexed, err)
	}
	ws, err := ReadWorkspace(profile, id)
	if err != nil || ws.GitRoot == "" || ws.Cwd != repo {
		t.Fatalf("workspace=%+v err=%v", ws, err)
	}
	after, _ := os.ReadFile(events)
	if info, err := os.Stat(events); err != nil || !info.ModTime().Equal(updated) {
		t.Fatalf("session time=%v err=%v", info.ModTime(), err)
	}
	added := string(after[len(before):])
	if !strings.HasPrefix(string(after), string(before)) || !strings.Contains(added, "Imported from Claude Code by Copilot Ready.") || !strings.Contains(added, IndexReply) {
		t.Fatalf("the imported history changed or the exchange is missing:\n%.2000s", added)
	}
	for _, kind := range []string{"session.compaction", "session.truncation", "tool.execution_start", "session.model_change"} {
		if strings.Contains(added, `"type":"`+kind) {
			t.Fatalf("the turn wrote %s", kind)
		}
	}
	if !strings.Contains(added, `"selectedModel":"claude-sonnet-4.6"`) {
		t.Fatalf("the turn did not use the profile's model:\n%.600s", added)
	}
	if _, err := os.Stat(filepath.Join(markers, "mcp")); err == nil {
		t.Fatal("an MCP server started during indexing")
	}
	// A hook the user already had in Copilot runs, as the opt-in text says.
	if _, err := os.Stat(filepath.Join(markers, "hook")); err != nil {
		t.Logf("the existing sessionStart hook did not run: %v", err)
	}
	logs, _ := filepath.Glob(filepath.Join(profile, "logs", "process-*.log"))
	left, _ := os.ReadDir(scratch)
	if len(logs) != 0 || len(left) != 0 {
		t.Fatalf("logs left: %v %v", logs, left)
	}
	t.Logf("history %d bytes, exchange %d bytes", len(before), len(added))
}
