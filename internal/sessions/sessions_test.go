package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestMain(m *testing.M) {
	if os.Getenv("FAKE_CODEX_APP_SERVER") == "1" {
		fakeAppServer()
		return
	}
	os.Exit(m.Run())
}

const claudeID = "11111111-2222-3333-4444-555555555555"

// claudeFixture holds a compaction, then a thinking block, parallel tool calls whose first result
// sits beside the main chain, an abandoned rewind branch, a sidechain and a raw request record.
func claudeFixture(t *testing.T, cwd string) string {
	t.Helper()
	root := t.TempDir()
	msg := func(role string, content any) map[string]any { return map[string]any{"role": role, "content": content} }
	asst := func(id string, blocks ...any) map[string]any {
		return map[string]any{"id": id, "model": "claude-test", "role": "assistant", "content": blocks}
	}
	lines := []map[string]any{
		{"type": "queue-operation", "operation": "enqueue"},
		{"type": "user", "uuid": "u0", "parentUuid": nil, "isMeta": true, "cwd": cwd, "gitBranch": "main", "timestamp": "2026-10-01T10:00:00Z", "message": msg("user", "<local-command-caveat>ignore</local-command-caveat>")},
		{"type": "user", "uuid": "p1", "parentUuid": "u0", "cwd": cwd, "timestamp": "2026-10-01T10:00:01Z", "message": msg("user", "Old request")},
		{"type": "assistant", "uuid": "p2", "parentUuid": "p1", "message": asst("m9", map[string]any{"type": "text", "text": "Old answer"})},
		{"type": "system", "subtype": "compact_boundary", "uuid": "c1", "parentUuid": nil, "logicalParentUuid": "p2"},
		{"type": "user", "uuid": "s1", "parentUuid": "c1", "isCompactSummary": true, "message": msg("user", "Summary of earlier work")},
		{"type": "user", "uuid": "u1", "parentUuid": "s1", "cwd": cwd, "message": msg("user", "Fix the build<system-reminder>internal note</system-reminder>")},
		{"type": "assistant", "uuid": "old", "parentUuid": "u1", "message": asst("m0", map[string]any{"type": "text", "text": "Abandoned branch"})},
		{"type": "assistant", "uuid": "a1", "parentUuid": "u1", "message": asst("m1", map[string]any{"type": "thinking", "thinking": "private plan", "signature": "sig"})},
		{"type": "assistant", "uuid": "a2", "parentUuid": "a1", "message": asst("m1", map[string]any{"type": "text", "text": "Running two checks."})},
		{"type": "assistant", "uuid": "a3", "parentUuid": "a2", "message": asst("m1", map[string]any{"type": "tool_use", "id": "tA", "name": "Bash", "input": map[string]any{"command": "go build"}})},
		{"type": "assistant", "uuid": "a4", "parentUuid": "a3", "message": asst("m1", map[string]any{"type": "tool_use", "id": "tB", "name": "Read", "input": map[string]any{"file_path": "x.go"}})},
		{"type": "user", "uuid": "r1", "parentUuid": "a3", "message": msg("user", []any{map[string]any{"type": "tool_result", "tool_use_id": "tA", "content": "build ok"}})},
		{"type": "user", "uuid": "r2", "parentUuid": "a4", "message": msg("user", []any{map[string]any{"type": "tool_result", "tool_use_id": "tB", "content": []any{map[string]any{"type": "text", "text": "denied"}}, "is_error": true}})},
		{"type": "api-request", "uuid": "x1", "body": "SYSTEM PROMPT"},
		{"type": "user", "uuid": "u2", "parentUuid": "r2", "message": msg("user", "Now add a test")},
		{"type": "assistant", "uuid": "a5", "parentUuid": "u2", "message": asst("m2", map[string]any{"type": "text", "text": "Added."})},
		{"type": "assistant", "uuid": "sc", "parentUuid": "a5", "isSidechain": true, "message": asst("m3", map[string]any{"type": "text", "text": "Subagent detail"})},
		{"type": "custom-title", "customTitle": `Fix "the" build`, "sessionId": claudeID},
	}
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	b.WriteString(`{"type":"user","uuid":"partial`) // a session still being written
	put(t, filepath.Join(root, "projects", "p", claudeID+".jsonl"), b.String())
	put(t, filepath.Join(root, "projects", "p", "not-a-session.jsonl"), "{}\n")
	return root
}

func put(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}

func records(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// shape lists each record as type[/role] so tests can compare the order Copilot will see.
func shape(recs []map[string]any) string {
	var parts []string
	for _, r := range recs[1:] {
		s := r["type"].(string)
		if role, ok := r["role"].(string); ok {
			s += "/" + role
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestClaudeTranscriptBecomesTheConversationClaudeWouldResume(t *testing.T) {
	cwd := t.TempDir()
	list := ListClaude(claudeFixture(t, cwd))
	if len(list) != 1 {
		t.Fatalf("sessions=%+v", list)
	}
	s := list[0]
	if s.ID != claudeID || s.Cwd != cwd || s.Branch != "main" || s.Title != `Fix "the" build` || !s.Created.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("session=%+v", s)
	}
	c, err := LoadClaude(s)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Interchange(c)
	if err != nil {
		t.Fatal(err)
	}
	recs := records(t, data)
	// Claude's summary of the compacted part opens the conversation; older messages stay out.
	want := "message/user message/user message/assistant tool-call tool-result tool-call tool-result message/user message/assistant"
	if got := shape(recs); got != want {
		t.Fatalf("got  %s\nwant %s\n%s", got, want, data)
	}
	h := recs[0]
	if h["externalId"] != "claude:"+claudeID || h["title"] != "Fix 'the' build" || h["source"].(map[string]any)["application"] != "claude-code" || h["workspace"].(map[string]any)["workingDirectory"] != cwd {
		t.Fatalf("header=%v", h)
	}
	text := func(i int) any { return recs[i]["content"].([]any)[0].(map[string]any)["text"] }
	if text(1) != "Summary of earlier work" || text(2) != "Fix the build" || recs[3]["model"] != "claude-test" {
		t.Fatalf("opening turns=%v %v %v", recs[1], recs[2], recs[3])
	}
	if recs[5]["success"] != true || recs[5]["content"] != "build ok" || recs[7]["success"] != false || recs[7]["error"].(map[string]any)["message"] != "denied" {
		t.Fatalf("results=%v %v", recs[5], recs[7])
	}
	for _, hidden := range []string{"Old request", "Old answer", "private plan", "SYSTEM PROMPT", "Abandoned branch", "internal note", "Subagent detail", "local-command"} {
		if strings.Contains(string(data), hidden) {
			t.Fatalf("%q leaked into the import:\n%s", hidden, data)
		}
	}
}

func TestInterchangeFollowsCopilotTurnRules(t *testing.T) {
	c := Conversation{Session: Session{Source: "codex", ID: "t1", Cwd: t.TempDir()}, Events: []Event{
		{Kind: "assistant", Text: "before any prompt"},
		{Kind: "user", Text: "go"},
		{Kind: "call", CallID: "c1", Name: "shell", Args: "ls"},
		{Kind: "result", CallID: "c1", Text: "a.txt"},
		{Kind: "call", CallID: "c2", Name: "shell", Args: map[string]any{"command": strings.Repeat("x", 10000)}},
		{Kind: "assistant", Text: "done"},
		{Kind: "assistant", Text: "really"},
		{Kind: "result", CallID: "unknown", Text: "ignored"},
	}}
	data, err := Interchange(c)
	if err != nil {
		t.Fatal(err)
	}
	recs := records(t, data)
	want := "message/user message/assistant tool-call tool-result tool-call tool-result message/assistant"
	if got := shape(recs); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if recs[2]["content"].([]any)[0].(map[string]any)["text"] != "" || recs[3]["arguments"].(map[string]any)["input"] != "ls" {
		t.Fatalf("tool-only turn=%v %v", recs[2], recs[3])
	}
	if recs[6]["success"] != false || recs[6]["toolCallId"] != recs[5]["id"] || len(recs[5]["arguments"].(map[string]any)["command"].(string)) > maxArg+100 {
		t.Fatalf("unanswered call=%v %v", recs[5], recs[6])
	}
	if recs[7]["content"].([]any)[0].(map[string]any)["text"] != "done\n\nreally" {
		t.Fatalf("merged reply=%v", recs[7])
	}
	if _, err = Interchange(Conversation{Session: c.Session, Events: []Event{{Kind: "assistant", Text: "alone"}}}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("err=%v", err)
	}
}

func TestTitlesFollowCopilotNameRules(t *testing.T) {
	for in, want := range map[string]string{
		"  first line \n second": "first line",
		"say \"hi\"\tnow":        "say 'hi' now",
		"\x1b[31mred":            "[31mred",
	} {
		if got := titleLine(in); got != want {
			t.Errorf("titleLine(%q)=%q want %q", in, got, want)
		}
	}
	long := titleLine(strings.Repeat("🚀", 80))
	if units := len(utf16.Encode([]rune(long))); units > 100 || !strings.HasSuffix(long, "…") {
		t.Fatalf("long title has %d UTF-16 units: %q", units, long)
	}
}

func TestCodexItemsBecomeMessagesAndToolCalls(t *testing.T) {
	var events []Event
	for _, item := range []string{
		`{"type":"userMessage","id":"1","content":[{"type":"text","text":"# Context\nopen files\n## My request for Codex:\nShip it"},{"type":"localImage","path":"a.png"}]}`,
		`{"type":"reasoning","id":"2","summary":["hidden"]}`,
		`{"type":"agentMessage","id":"3","text":"On it"}`,
		`{"type":"commandExecution","id":"4","command":"go test","cwd":"/w","aggregatedOutput":"FAIL","exitCode":1,"status":"completed","commandActions":[]}`,
		`{"type":"mcpToolCall","id":"5","server":"docs","tool":"search","arguments":{"q":"x"},"status":"completed","result":{"content":[{"type":"text","text":"found"}]}}`,
		`{"type":"fileChange","id":"6","status":"completed","changes":[{"path":"a.go","kind":{"type":"update"},"diff":"+x"}]}`,
		`{"type":"webSearch","id":"7","query":"copilot"}`,
		`{"type":"contextCompaction","id":"8"}`,
	} {
		events = append(events, codexEvents(json.RawMessage(item))...)
	}
	var got []string
	for _, e := range events {
		got = append(got, fmt.Sprintf("%s:%s:%s:%v", e.Kind, e.Name, e.Text, e.Failed))
	}
	want := []string{
		"user::Ship it\n[image not imported]:false", "assistant::On it:false",
		"call:shell::false", "result::FAIL:true",
		"call:docs.search::false", "result::found:false",
		"call:apply_patch::false", "result::+x:false",
		"call:web_search::false", "result:::false",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestCodexClientPagesThreadsAndTurns(t *testing.T) {
	t.Setenv("FAKE_CODEX_APP_SERVER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := StartCodex(ctx, exe, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	list, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Title != "Named thread" || list[1].Title != "Ship it" || list[0].Branch != "dev" || list[0].Created.Unix() != 1790000000 || list[1].Updated.UnixMilli() != 1790000001234 {
		t.Fatalf("threads=%+v", list)
	}
	conv, err := c.Load(list[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Events) != 4 || conv.Events[0].Text != "hello" || conv.Events[2].Name != "shell" {
		t.Fatalf("events=%+v", conv.Events)
	}
}

// fakeAppServer answers like `codex app-server` and interleaves notifications with replies.
func fakeAppServer() {
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		var req struct {
			ID     *int           `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		fmt.Println(`{"method":"thread/status/changed","params":{}}`)
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			if req.Params["capabilities"].(map[string]any)["experimentalApi"] != true {
				fmt.Printf(`{"id":%d,"error":{"message":"requires experimentalApi capability"}}`+"\n", *req.ID)
				continue
			}
		case "thread/list":
			switch {
			case req.Params["archived"] == true:
				result = map[string]any{"data": []any{}}
			case req.Params["cursor"] == nil:
				result = map[string]any{"data": []any{map[string]any{"id": "t1", "name": "Named thread", "preview": "x", "cwd": "/w", "createdAt": 1790000000, "updatedAt": 1790000005, "gitInfo": map[string]any{"branch": "dev"}}}, "nextCursor": "page2"}
			default:
				result = map[string]any{"data": []any{
					map[string]any{"id": "t2", "preview": "## My request for Codex:\nShip it", "cwd": "/w", "createdAt": 1790000000000, "updatedAt": 1790000001234},
					map[string]any{"id": "t3", "preview": "temp", "cwd": "/w", "ephemeral": true},
				}}
			}
		case "thread/turns/list":
			if req.Params["sortDirection"] != "asc" || req.Params["itemsView"] != "full" {
				fmt.Printf(`{"id":%d,"error":{"message":"unexpected paging"}}`+"\n", *req.ID)
				continue
			}
			if req.Params["cursor"] == nil {
				result = map[string]any{"data": []any{map[string]any{"id": "turn1", "status": "completed", "items": []any{
					map[string]any{"type": "userMessage", "id": "i1", "content": []any{map[string]any{"type": "text", "text": "hello"}}},
					map[string]any{"type": "agentMessage", "id": "i2", "text": "hi"},
				}}}, "nextCursor": "turn2"}
			} else {
				result = map[string]any{"data": []any{map[string]any{"id": "turn2", "status": "completed", "items": []any{
					map[string]any{"type": "commandExecution", "id": "i3", "command": "ls", "cwd": "/w", "status": "completed", "exitCode": 0, "commandActions": []any{}},
				}}}}
			}
		}
		b, _ := json.Marshal(map[string]any{"id": *req.ID, "result": result})
		fmt.Println(string(b))
	}
}

func TestGitRepositoryComesFromTheFolder(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:octo/hello.git":           "octo/hello",
		"https://github.com/octo/hello":           "octo/hello",
		"https://user@github.com/octo/hello.git/": "octo/hello",
		"ssh://git@github.com/octo/hello.git":     "octo/hello",
		"https://gitlab.com/octo/hello.git":       "",
		"https://dev.azure.com/org/p/_git/repo":   "",
	} {
		owner, name, _ := githubRepository(remote)
		if got := strings.Trim(owner+"/"+name, "/"); got != want {
			t.Errorf("%s: got %q want %q", remote, got, want)
		}
	}
	repo := t.TempDir()
	put(t, filepath.Join(repo, ".git", "config"), "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = git@github.com:other/x.git\n[remote \"origin\"]\n\turl = git@github.com:octo/hello.git\n")
	put(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/feature\n")
	put(t, filepath.Join(repo, ".git", "worktrees", "wt", "HEAD"), "ref: refs/heads/wt-branch\n")
	put(t, filepath.Join(repo, ".git", "worktrees", "wt", "commondir"), "../..\n")
	put(t, filepath.Join(repo, "wt", ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n")
	if err := os.MkdirAll(filepath.Join(repo, "src", "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	if origin, branch := gitInfo(filepath.Join(repo, "src", "pkg")); origin != "git@github.com:octo/hello.git" || branch != "feature" {
		t.Fatalf("origin=%q branch=%q", origin, branch)
	}
	if origin, branch := gitInfo(filepath.Join(repo, "wt")); origin != "git@github.com:octo/hello.git" || branch != "wt-branch" {
		t.Fatalf("worktree origin=%q branch=%q", origin, branch)
	}
	data, err := Interchange(Conversation{Session: Session{Source: "claude", ID: "x", Cwd: filepath.Join(repo, "src")}, Events: []Event{{Kind: "user", Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	ws := records(t, data)[0]["workspace"].(map[string]any)
	if ws["branch"] != "feature" || ws["repository"].(map[string]any)["owner"] != "octo" {
		t.Fatalf("workspace=%v", ws)
	}
}

func TestRecordRemembersImportsPerProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "imported.json")
	if err := OpenRecord(path).Add("/profile/a", "claude:1", "copilot-1"); err != nil {
		t.Fatal(err)
	}
	r := OpenRecord(path)
	if !r.Has("/profile/a", "claude:1") || r.Has("/profile/b", "claude:1") || r.Has("/profile/a", "codex:1") {
		t.Fatal("record lookup")
	}
	// Two setup windows writing at once keep each other's entries and see each other's imports.
	other := OpenRecord(path)
	if err := r.Add("/profile/a", "codex:1", "copilot-2"); err != nil {
		t.Fatal(err)
	}
	if err := other.Add("/profile/a", "claude:2", "copilot-3"); err != nil {
		t.Fatal(err)
	}
	if !other.Has("/profile/a", "codex:1") || !OpenRecord(path).Has("/profile/a", "codex:1") || !OpenRecord(path).Has("/profile/a", "claude:2") {
		t.Fatal("concurrent record lost an import")
	}
}

// TestCopilotAcceptsTheImports runs Copilot's own validator when a Copilot CLI with
// `sessions import` (1.0.85 or later) is on PATH. Dry runs write nothing.
func TestCopilotAcceptsTheImports(t *testing.T) {
	copilot, err := exec.LookPath("copilot")
	if err != nil || exec.Command(copilot, "--no-auto-update", "sessions", "import", "--help").Run() != nil {
		t.Skip("Copilot CLI with sessions import is not on PATH")
	}
	cwd := t.TempDir()
	claude, err := LoadClaude(ListClaude(claudeFixture(t, cwd))[0])
	if err != nil {
		t.Fatal(err)
	}
	codex := Conversation{Session: Session{Source: "codex", ID: "t1", Cwd: cwd, Title: strings.Repeat("long title ", 20)}}
	for _, item := range []string{
		`{"type":"userMessage","id":"1","content":[{"type":"text","text":"Ship it"}]}`,
		`{"type":"commandExecution","id":"2","command":"go test","cwd":"/w","aggregatedOutput":"FAIL","exitCode":1,"status":"completed","commandActions":[]}`,
		`{"type":"mcpToolCall","id":"3","server":"docs","tool":"search","arguments":["not","an","object"],"status":"completed"}`,
		`{"type":"agentMessage","id":"4","text":"Done"}`,
	} {
		codex.Events = append(codex.Events, codexEvents(json.RawMessage(item))...)
	}
	for _, c := range []Conversation{claude, codex, oversizedRequest(cwd)} {
		data, err := Interchange(c)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(t.TempDir(), "conversation.jsonl")
		put(t, file, string(data))
		cmd := exec.Command(copilot, "--no-auto-update", "sessions", "import", "--dry-run", "--output", "json", file)
		cmd.Env = append(os.Environ(), "COPILOT_HOME="+t.TempDir())
		out, _ := cmd.CombinedOutput()
		if !strings.Contains(string(out), `"ok":true`) {
			t.Fatalf("%s rejected: %s\n%s", c.Source, out, data)
		}
	}
}

func TestClaudeTitleRightAfterThePromptIsKept(t *testing.T) {
	cwd := t.TempDir()
	root := t.TempDir()
	prompt, _ := json.Marshal(map[string]any{"type": "user", "uuid": "u1", "cwd": cwd, "message": map[string]any{"role": "user", "content": "First prompt"}})
	title, _ := json.Marshal(map[string]any{"type": "custom-title", "customTitle": "Saved title"})
	put(t, filepath.Join(root, "projects", "p", claudeID+".jsonl"), string(prompt)+"\n"+string(title)+"\n")
	if list := ListClaude(root); len(list) != 1 || list[0].Title != "Saved title" {
		t.Fatalf("sessions=%+v", list)
	}
}

func TestLongHistoryKeepsTheNewestTurnsAndCleansToolNames(t *testing.T) {
	c := Conversation{Session: Session{Source: "codex", ID: "long", Cwd: t.TempDir()}}
	for i := range 400 {
		c.Events = append(c.Events, Event{Kind: "user", Text: fmt.Sprintf("request %d", i)}, Event{Kind: "assistant", Text: strings.Repeat("x", 2000)})
	}
	c.Events = append(c.Events, Event{Kind: "call", CallID: "c", Name: "docs.search tool/v2"}, Event{Kind: "result", CallID: "c", Text: "ok"})
	data, err := Interchange(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxHistory+4096 {
		t.Fatalf("import is %d bytes", len(data))
	}
	recs := records(t, data)
	opening := recs[1]["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.HasPrefix(opening, "[Imported from Codex. The ") || !strings.Contains(opening, "earlier turns are left out") {
		t.Fatalf("opening=%q", opening[:min(len(opening), 120)])
	}
	last := recs[len(recs)-4] // the newest user message, before its reply, call and result
	if last["content"].([]any)[0].(map[string]any)["text"] != "request 399" || recs[len(recs)-2]["name"] != "docs_search_tool_v2" {
		t.Fatalf("newest turn or tool name: %v %v", last, recs[len(recs)-2])
	}
}

// oversizedRequest is one request whose tool output alone is several times the history budget.
func oversizedRequest(cwd string) Conversation {
	c := Conversation{Session: Session{Source: "claude", ID: "big", Cwd: cwd}, Events: []Event{{Kind: "user", Text: "Run every check"}}}
	for i := range 120 {
		id := fmt.Sprintf("c%d", i)
		c.Events = append(c.Events, Event{Kind: "call", CallID: id, Name: "Bash", Args: map[string]any{"command": "check"}}, Event{Kind: "result", CallID: id, Text: strings.Repeat("y", 16000)})
	}
	c.Events = append(c.Events, Event{Kind: "assistant", Text: "All checks done."})
	return c
}

func TestOversizedRequestKeepsItsNewestStepsWithinBudget(t *testing.T) {
	data, err := Interchange(oversizedRequest(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxHistory+4096 {
		t.Fatalf("import is %d bytes", len(data))
	}
	recs := records(t, data)
	request := recs[1]["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(request, "earlier steps of this request are left out") || !strings.HasSuffix(request, "Run every check") {
		t.Fatalf("request=%q", request[:min(len(request), 120)])
	}
	if recs[2]["type"] != "message" || recs[2]["role"] != "assistant" || recs[3]["type"] != "tool-call" || recs[4]["toolCallId"] != recs[3]["id"] {
		t.Fatalf("first kept step: %v %v %v", recs[2]["type"], recs[3]["type"], recs[4])
	}
	if recs[len(recs)-1]["content"].([]any)[0].(map[string]any)["text"] != "All checks done." {
		t.Fatal("final reply dropped")
	}
}
