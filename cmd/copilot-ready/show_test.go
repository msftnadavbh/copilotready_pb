package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"copilotready/internal/installer"
	"copilotready/internal/migration"
	"copilotready/internal/sessions"
)

// The test binary doubles as Copilot: with FAKE_COPILOT set to a folder, it answers the commands
// setup runs and logs each one there, so tests see what ran, in which order and with what.
func TestMain(m *testing.M) {
	if dir := os.Getenv("FAKE_COPILOT"); dir != "" {
		os.Exit(fakeCopilot(dir, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeCopilot(dir string, args []string) int {
	logf := func(format string, a ...any) {
		f, _ := os.OpenFile(filepath.Join(dir, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		fmt.Fprintf(f, format+"\n", a...)
		f.Close()
	}
	profile := os.Getenv("COPILOT_HOME")
	var rest, disabled []string
	logDir := ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--no-auto-update":
		case args[i] == "--log-dir" && i+1 < len(args):
			i++
			logDir = args[i]
		case args[i] == "--disable-mcp-server" && i+1 < len(args):
			i++
			disabled = append(disabled, args[i])
		default:
			rest = append(rest, args[i])
		}
	}
	// Copilot writes a log for every process. One that ignored --log-dir would land in the profile.
	_ = os.MkdirAll(filepath.Join(profile, "logs"), 0700)
	_ = os.WriteFile(filepath.Join(profile, "logs", fmt.Sprintf("process-%d-%d.log", time.Now().UnixMilli(), os.Getpid())), []byte("log"), 0600)
	if logDir == "" && (len(rest) == 0 || rest[0] != "plugin") {
		logf("no --log-dir")
	}
	hooks, _ := filepath.Glob(filepath.Join(profile, "hooks", "copilot-ready-*.json"))
	_, pluginErr := os.Stat(filepath.Join(dir, "plugin-installed"))
	switch {
	case len(rest) > 2 && rest[0] == "sessions" && rest[1] == "import":
		data, _ := os.ReadFile(rest[len(rest)-1])
		var header struct {
			ExternalID string
			Workspace  struct{ WorkingDirectory string }
		}
		_ = json.Unmarshal(data[:bytes.IndexByte(data, '\n')], &header)
		// These ids are rows of the session index fixture, which appears with the first turn.
		var n int
		_, _ = fmt.Sscanf(header.ExternalID, "claude:%d", &n)
		id := fmt.Sprintf("main-%04d", n)
		state := filepath.Join(profile, "session-state", id)
		_ = os.MkdirAll(state, 0700)
		_ = os.WriteFile(filepath.Join(state, "events.jsonl"), []byte("{\"type\":\"session.start\"}\n"), 0600)
		_ = os.WriteFile(filepath.Join(state, "workspace.yaml"), []byte("id: "+id+"\ncwd: '"+header.Workspace.WorkingDirectory+"'\nname: Imported "+id+"\n"), 0600)
		fmt.Printf(`{"ok":true,"sessionId":%q}`, id)
		logf("import %s", id)
	case len(rest) > 1 && rest[0] == "mcp" && rest[1] == "list":
		fmt.Print(`{"mcpServers":{"user-server":{"source":"user"},"plugin-server":{"source":"plugin"}}}`)
		logf("mcp list")
	case len(rest) > 0 && strings.HasPrefix(rest[0], "--resume="):
		id := strings.TrimPrefix(rest[0], "--resume=")
		index := filepath.Join(profile, "session-store.db")
		if _, err := os.Stat(index); err != nil {
			for _, suffix := range []string{"", "-wal"} {
				b, _ := os.ReadFile(os.Getenv("FAKE_COPILOT_INDEX") + suffix)
				_ = os.WriteFile(index+suffix, b, 0600)
			}
		}
		// Ask the stand-in model, the way Copilot's offline provider mode does.
		body, _ := json.Marshal(map[string]any{"model": os.Getenv("COPILOT_MODEL"), "stream": false, "messages": []any{map[string]any{"role": "user", "content": rest[2]}}})
		req, _ := http.NewRequest("POST", os.Getenv("COPILOT_PROVIDER_BASE_URL")+"/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+os.Getenv("COPILOT_PROVIDER_API_KEY"))
		answer := "no answer"
		if resp, err := http.DefaultClient.Do(req); err == nil {
			var r struct {
				Choices []struct{ Message struct{ Content string } }
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if json.Unmarshal(b, &r) == nil && len(r.Choices) == 1 {
				answer = r.Choices[0].Message.Content
			}
		}
		events := filepath.Join(profile, "session-state", id, "events.jsonl")
		f, _ := os.OpenFile(events, os.O_APPEND|os.O_WRONLY, 0600)
		fmt.Fprintf(f, "{\"type\":\"user.message\"}\n{\"type\":\"assistant.message\"}\n")
		if os.Getenv("FAKE_COPILOT_COMPACTS") == id {
			fmt.Fprintf(f, "{\"type\":\"session.compaction_start\"}\n")
		}
		f.Close()
		cwd, _ := os.Getwd()
		slices.Sort(disabled)
		logf("resume %s prompt=%q answer=%q hooks=%d plugin=%v off=%s offline=%s model=%s token=%q proxy=%q cwd=%s", id, rest[2], answer, len(hooks), pluginErr == nil,
			strings.Join(disabled, ","), os.Getenv("COPILOT_OFFLINE"), os.Getenv("COPILOT_MODEL"), os.Getenv("GH_TOKEN"), os.Getenv("HTTPS_PROXY"), cwd)
	case len(rest) > 0 && rest[0] == "plugin":
		if slices.Contains(rest, "install") {
			_ = os.WriteFile(filepath.Join(dir, "plugin-installed"), nil, 0600)
		}
		logf("plugin %s hooks=%d", strings.Join(rest[1:], " "), len(hooks))
	default:
		logf("other %s", strings.Join(rest, " "))
	}
	return 0
}

func calls(t *testing.T, dir string) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

type showFixture struct {
	a                   *app
	fake, profile, work string
	report              migration.Report
	list                []sessions.Session
	updated             time.Time
}

func newShowFixture(t *testing.T) showFixture {
	t.Helper()
	for _, k := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "COPILOT_HOME"} {
		t.Setenv(k, "")
	}
	f := showFixture{fake: t.TempDir(), work: t.TempDir(), updated: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)}
	t.Setenv("FAKE_COPILOT", f.fake)
	index, _ := filepath.Abs(filepath.Join("..", "..", "internal", "sessions", "testdata", "session-store.db"))
	t.Setenv("FAKE_COPILOT_INDEX", index)
	// Neither may reach the turn: it must use no account and send nothing through a proxy.
	t.Setenv("GH_TOKEN", "must-not-reach-copilot")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	home := t.TempDir()
	root := filepath.Join(home, "ready")
	f.profile = filepath.Join(root, "profiles", "default")
	f.a = &app{root: root, manager: installer.New(root, home)}
	if err := f.a.manager.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	// A plugin from a local marketplace, so setup can install it with Copilot's plugin command.
	market := filepath.Join(home, "markets", "team")
	put := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(market, ".claude-plugin", "marketplace.json"), `{"name":"team","plugins":[]}`)
	put(filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"), `{"team":{"source":{"source":"directory","path":"`+filepath.ToSlash(market)+`"}}}`)
	put(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{"version":2,"plugins":{"helper@team":[{}]}}`)
	report, err := migration.Discover(migration.Options{Home: home, Project: f.work, Destination: f.profile, Mode: "new", SessionRecord: filepath.Join(root, "imported-sessions.json")})
	if err != nil {
		t.Fatal(err)
	}
	f.report = report
	for i, text := range []string{"Fix the build", "Write the docs"} {
		line, _ := json.Marshal(map[string]any{"type": "user", "uuid": "u1", "cwd": f.work, "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user", "content": text}})
		p := filepath.Join(home, ".claude", "projects", "p", fmt.Sprintf("%d.jsonl", i))
		put(p, string(line)+"\n")
		f.list = append(f.list, sessions.Session{Source: "claude", ID: fmt.Sprint(i), Title: text, Cwd: f.work, Path: p, Updated: f.updated})
	}
	// Logs the user already has stay, including one whose name ends like a process id.
	put(filepath.Join(f.profile, "logs", "process-1-4242.log"), "user log")
	put(filepath.Join(f.profile, "logs", "notes.log"), "user log")
	return f
}

func (f showFixture) run(t *testing.T, later afterShowing) *importProgress {
	t.Helper()
	exe, _ := os.Executable()
	f.a.imports = &importProgress{Running: true, Total: len(f.list), Failures: []string{}}
	f.a.result = map[string]any{"validation": []map[string]string{}}
	f.a.importConversations(exe, f.profile, f.report.Options, f.list, true, later)
	return f.a.imports
}

func TestConversationsAreShownBeforeImportedHooksAndPluginsCanRun(t *testing.T) {
	f := newShowFixture(t)
	hook := migration.Activation{Kind: "hook", Name: "claude-Stop", HookSource: "claude", HookEvent: "Stop", Config: map[string]any{"entries": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo stop"}}}}}}
	hooks, err := f.a.hookChanges(f.profile, []migration.Activation{hook})
	if err != nil || len(hooks) == 0 {
		t.Fatalf("hooks=%d err=%v", len(hooks), err)
	}
	var plugins []string
	for _, item := range f.report.Items {
		if item.ActivationKind == "plugin" && item.Selectable {
			plugins = append(plugins, item.ID)
		}
	}
	if len(plugins) != 1 {
		t.Fatalf("plugin fixture: %+v", f.report.Items)
	}
	p := f.run(t, afterShowing{report: &f.report, hooks: hooks, plugins: plugins, roots: []string{f.profile, f.a.root}, project: f.work})
	if p.Running || p.Imported != 2 || p.ShowTotal != 2 || p.Shown != 2 || p.ShowFailed != 0 || len(p.Failures) != 0 {
		t.Fatalf("progress=%+v", p)
	}
	log := calls(t, f.fake)
	var order []string
	for _, line := range log {
		order = append(order, strings.Fields(line)[0])
		if strings.HasPrefix(line, "resume ") {
			want := fmt.Sprintf(`prompt="Imported from Claude Code by Copilot Ready." answer="Ready to continue." hooks=0 plugin=false off=plugin-server,user-server offline=true model=auto token="" proxy="" cwd=%s`, f.work)
			if !strings.HasSuffix(line, want) {
				t.Fatalf("turn ran as\n%s\nwant suffix\n%s", line, want)
			}
		}
		if line == "no --log-dir" {
			t.Fatal("a conversation run wrote its log into the profile")
		}
	}
	if strings.Join(order, " ") != "import import mcp resume resume plugin plugin" {
		t.Fatalf("order=%v\n%s", order, strings.Join(log, "\n"))
	}
	if !strings.HasSuffix(log[len(log)-1], "hooks=1") {
		t.Fatalf("hooks must be installed before plugins, after the turns: %s", log[len(log)-1])
	}
	validation := f.a.result.(map[string]any)["validation"].([]map[string]string)
	if len(validation) != 2 || validation[0]["name"] != "Hooks" || validation[0]["status"] != "passed" || validation[1]["status"] != "passed" {
		t.Fatalf("validation=%v", validation)
	}
	// The logs of the import, MCP listing and turn runs are removed. The user's own logs and those
	// of the two plugin commands, which are not part of showing conversations, stay.
	logs, _ := os.ReadDir(filepath.Join(f.profile, "logs"))
	var names []string
	for _, e := range logs {
		names = append(names, e.Name())
	}
	if len(names) != 4 || names[0] != "notes.log" || names[1] != "process-1-4242.log" {
		t.Fatalf("logs left: %v", names)
	}
	shown := sessions.OpenRecord(filepath.Join(f.a.root, "indexed-sessions.json"))
	for _, id := range []string{"main-0000", "main-0001"} {
		if info, err := os.Stat(filepath.Join(f.profile, "session-state", id, "events.jsonl")); err != nil || !info.ModTime().Equal(f.updated) {
			t.Fatalf("%s time=%v err=%v", id, info.ModTime(), err)
		}
		if !shown.Has(f.profile, id) {
			t.Fatalf("%s was not recorded as shown", id)
		}
	}
	conversations, _ := filepath.Glob(filepath.Join(f.a.root, "conversations-*"))
	if leftovers, _ := filepath.Glob(filepath.Join(f.a.root, "show-*")); len(leftovers)+len(conversations) != 0 {
		t.Fatalf("scratch folders left: %v %v", leftovers, conversations)
	}

	// Running setup again imports nothing and adds no second exchange.
	_ = os.Remove(filepath.Join(f.fake, "calls.log"))
	p = f.run(t, afterShowing{})
	if p.Skipped != 2 || p.Shown != 2 || p.ShowDone != 2 {
		t.Fatalf("rerun progress=%+v", p)
	}
	if b, _ := os.ReadFile(filepath.Join(f.fake, "calls.log")); len(b) != 0 {
		t.Fatalf("rerun ran Copilot: %s", b)
	}
}

func TestConversationsInCopilotsIndexAreNotGivenAnotherTurn(t *testing.T) {
	f := newShowFixture(t)
	// Both are already in Copilot's index, as after the user continued them in the CLI.
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "sessions", "testdata", "session-store.db"+suffix))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(f.profile, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(f.profile, "session-store.db"+suffix), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := f.run(t, afterShowing{})
	if p.Shown != 2 || p.ShowFailed != 0 {
		t.Fatalf("progress=%+v", p)
	}
	for _, line := range calls(t, f.fake) {
		if strings.HasPrefix(line, "resume") {
			t.Fatalf("an indexed conversation got another turn: %s", line)
		}
	}
}

func TestACompactedConversationIsReported(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("FAKE_COPILOT_COMPACTS", "main-0001")
	p := f.run(t, afterShowing{})
	if p.Shown != 2 || len(p.Failures) != 1 || !strings.Contains(p.Failures[0], "shortened") {
		t.Fatalf("progress=%+v", p)
	}
}

func TestAnUnreadableIndexRunsNoTurns(t *testing.T) {
	f := newShowFixture(t)
	if err := os.MkdirAll(f.profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.profile, "session-store.db"), []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	p := f.run(t, afterShowing{})
	if p.Imported != 2 || p.Shown != 0 || p.ShowFailed != 2 || p.ShowDone != 2 || !strings.Contains(p.Failures[0], "nothing was run") {
		t.Fatalf("progress=%+v", p)
	}
	for _, line := range calls(t, f.fake) {
		if strings.HasPrefix(line, "resume") || strings.HasPrefix(line, "mcp") {
			t.Fatalf("Copilot ran although its index could not be read: %s", line)
		}
	}
}

func TestTheMaintenanceActionReportsConversationsAddedWithAProblem(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("COPILOT_HOME", f.profile) // the profile the Copilot app reads
	t.Setenv("FAKE_COPILOT_COMPACTS", "main-0001")
	// A managed runtime, as an earlier setup leaves it; the copy of this test binary plays Copilot.
	exe, _ := os.Executable()
	runtimePath := filepath.Join(f.a.root, "runtimes", "fake", filepath.Base(exe))
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if err = os.MkdirAll(filepath.Dir(runtimePath), 0700); err == nil {
		err = os.WriteFile(runtimePath, data, 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(map[string]any{"current": installer.Runtime{Version: "1.0.105", Path: runtimePath, SHA256: hex.EncodeToString(sum[:]), Platform: "test"}})
	if err = os.WriteFile(filepath.Join(f.a.root, "runtime-state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	// Two conversations imported by 2.0.9, not yet in the app.
	record := sessions.OpenRecord(filepath.Join(f.a.root, "imported-sessions.json"))
	for i, id := range []string{"main-0000", "main-0001"} {
		dir := filepath.Join(f.profile, "session-state", id)
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{\"type\":\"session.start\"}\n"), 0600)
		_ = os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("cwd: '"+f.work+"'\nname: Imported "+id+"\n"), 0600)
		if err = record.Add(f.profile, fmt.Sprintf("claude:%d", i), id); err != nil {
			t.Fatal(err)
		}
	}
	msg, err := f.a.showImported()
	if err != nil || !strings.HasPrefix(msg, "2 of 2 imported conversations are ready") || !strings.Contains(msg, "Added to the Copilot app with a problem: Imported main-0001") || !strings.Contains(msg, "shortened") {
		t.Fatalf("message=%q err=%v", msg, err)
	}
}

func TestTheMaintenanceActionSkipsASeparateProfile(t *testing.T) {
	f := newShowFixture(t)
	t.Setenv("COPILOT_HOME", filepath.Join(t.TempDir(), ".copilot"))
	exe, _ := os.Executable()
	runtimePath := filepath.Join(f.a.root, "runtimes", "fake", filepath.Base(exe))
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if err = os.MkdirAll(filepath.Dir(runtimePath), 0700); err == nil {
		err = os.WriteFile(runtimePath, data, 0700)
	}
	if err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(map[string]any{"current": installer.Runtime{Version: "1.0.105", Path: runtimePath, SHA256: hex.EncodeToString(sum[:]), Platform: "test"}})
	if err = os.WriteFile(filepath.Join(f.a.root, "runtime-state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.profile, "session-state", "main-0000")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{\"type\":\"session.start\"}\n"), 0600)
	if err = sessions.OpenRecord(filepath.Join(f.a.root, "imported-sessions.json")).Add(f.profile, "claude:0", "main-0000"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.showImported(); err == nil || !strings.Contains(err.Error(), "separate Copilot profile") {
		t.Fatalf("separate profile: %v", err)
	}
	for _, line := range calls(t, f.fake) {
		if strings.HasPrefix(line, "resume") {
			t.Fatalf("Copilot ran for a profile the app doesn't read: %s", line)
		}
	}
}

func TestShowWithNothingToAddSaysSoOnce(t *testing.T) {
	a, _, _ := openAppFixture(t, 0)
	if msg := a.thenOpenInApp(""); msg != "There are no imported conversations to show. Conversations are imported when you run setup." {
		t.Fatalf("message: %q", msg)
	}
}
