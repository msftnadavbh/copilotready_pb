package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCursor(t *testing.T, path string, lines ...any) {
	t.Helper()
	var b bytes.Buffer
	for _, l := range lines {
		if s, ok := l.(string); ok {
			b.WriteString(s + "\n")
			continue
		}
		j, _ := json.Marshal(l)
		b.Write(append(j, '\n'))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func cursorLine(role string, content any) map[string]any {
	return map[string]any{"role": role, "message": map[string]any{"content": content}}
}

func TestCursorFolderMatchesHyphensDotsAndSpaces(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"my-app/web", "my/app/web", "My App.v2/src", "a/b", "x  y/z"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(p)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{
		"":              "",
		"my-app-web":    "", // my-app/web and my/app/web both match, so neither is chosen
		"My-App-v2-src": "My App.v2/src",
		"my-app-web-x":  "",
		"a-b":           "a/b",
		"a":             "a",
		"-a-b-":         "a/b",
		"x-y-z":         "x  y/z",
		"x--y-z":        "x  y/z",
		"zzz":           "",
	} {
		got := CursorFolder(name, []string{root})
		if want != "" {
			want = filepath.Join(root, filepath.FromSlash(want))
		}
		if got != want {
			t.Errorf("%s: got %q want %q", name, got, want)
		}
	}
}

func TestCursorTranscriptsListOnceAndSkipSubagents(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "projects", "p", "agent-transcripts")
	query := []any{map[string]any{"type": "text", "text": "<timestamp>Monday</timestamp>\n<user_query>\nAdd a login page\n</user_query>"}}
	first := cursorLine("user", query)
	first["cwd"], first["timestamp_ms"] = work, 1760000000000
	writeCursor(t, filepath.Join(dir, "abc", "abc.jsonl"), first)
	writeCursor(t, filepath.Join(dir, "abc", "subagents", "sub.jsonl"), cursorLine("user", "Helper task"))
	writeCursor(t, filepath.Join(dir, "flat.jsonl"), `{"role":"assistant","message":{"content":"no user turn"}}`)
	writeCursor(t, filepath.Join(dir, "cli.jsonl"), map[string]any{"role": "user", "cwd": work, "message": map[string]any{"content": "Plain text prompt"}})
	writeCursor(t, filepath.Join(dir, "meta.jsonl"), map[string]any{"role": "user", "isMeta": true, "cwd": work, "message": map[string]any{"content": "Not typed by the user"}})
	timed := cursorLine("user", "Timed")
	timed["cwd"], timed["timestamp"] = work, "2026-10-01T10:00:00Z"
	writeCursor(t, filepath.Join(dir, "timed.jsonl"), timed)
	writeCursor(t, filepath.Join(home, "projects", "empty-window", "agent-transcripts", "loose.jsonl"), cursorLine("user", "No folder"))
	homeChat := cursorLine("user", "No folder, home recorded")
	homeChat["cwd"] = filepath.Dir(home)
	writeCursor(t, filepath.Join(home, "projects", "empty-window", "agent-transcripts", "home.jsonl"), homeChat)
	later := cursorLine("assistant", "Looking.")
	later["cwd"] = work
	writeCursor(t, filepath.Join(dir, "later.jsonl"), cursorLine("user", "Folder recorded later"), later)
	list := ListCursor(filepath.Join(home, ".cursor"))
	if len(list) != 0 {
		t.Fatalf("nothing outside root/projects: %+v", list)
	}
	list = ListCursor(home)
	if len(list) != 6 {
		t.Fatalf("list=%+v", list)
	}
	byID := map[string]Session{}
	for _, s := range list {
		byID[s.ID] = s
	}
	abc := byID["abc"]
	if abc.Title != "Add a login page" || abc.Cwd != work || abc.Created.UnixMilli() != 1760000000000 || abc.ExternalID() != "cursor:abc" {
		t.Fatalf("abc=%+v", abc)
	}
	if cli := byID["cli"]; cli.Title != "Plain text prompt" || !cli.Created.Equal(cli.Updated) {
		t.Fatalf("a transcript without times is dated by the file: %+v", cli)
	}
	if timed := byID["timed"]; timed.Created.Format("2006-01-02") != "2026-10-01" {
		t.Fatalf("timed=%+v", timed)
	}
	if loose := byID["loose"]; loose.Cwd != filepath.Dir(home) || !loose.Loose || abc.Loose {
		t.Fatalf("a chat outside any folder continues in the folder holding Cursor's: %+v", loose)
	}
	if chat := byID["home"]; !chat.Loose {
		t.Fatalf("a chat outside any folder stays loose when Cursor recorded the home folder: %+v", chat)
	}
	if chat := byID["later"]; chat.Cwd != work || chat.Loose {
		t.Fatalf("a folder recorded after the first prompt is used: %+v", chat)
	}
}

func TestCursorConversationKeepsTextAndNamesTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeCursor(t, path,
		cursorLine("assistant", "dropped: nothing before the first prompt"),
		cursorLine("user", []any{map[string]any{"type": "text", "text": "<cursor_commands>/review</cursor_commands>\nRun the tests"}}),
		cursorLine("assistant", []any{
			map[string]any{"type": "text", "text": "Running them."},
			map[string]any{"type": "tool_use", "name": "Shell", "input": map[string]any{"command": "go   test\n./...", "description": "tests"}},
			map[string]any{"type": "tool_use", "name": "Write", "input": map[string]any{"path": "a`b.go", "contents": strings.Repeat("x", 5000)}},
			map[string]any{"type": "tool_use", "name": "Think"},
		}),
		"not json",
		map[string]any{"type": "turn_ended"},
		cursorLine("user", "<user_query>Thanks</user_query>"),
	)
	c, err := LoadCursor(Session{Source: "cursor", ID: "t", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Events) != 3 || c.Events[0].Kind != "user" || c.Events[1].Kind != "assistant" || c.Events[2].Text != "Thanks" {
		t.Fatalf("events=%+v", c.Events)
	}
	if c.Events[0].Text != "Run the tests" || c.Note != cursorNote {
		t.Fatalf("first=%q note=%q", c.Events[0].Text, c.Note)
	}
	reply := c.Events[1].Text
	for _, want := range []string{"Running them.", "_Used Shell: `go test ./...`_", "_Used Write: `a'b.go`_", "_Used Think_"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply lacks %q: %s", want, reply)
		}
	}
	if strings.Contains(reply, "xxxx") {
		t.Fatal("tool input other than what it acted on is left out")
	}
	b, err := Interchange(c)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"tool-call"`)) || !bytes.Contains(b, []byte(`"application":"cursor"`)) || !bytes.Contains(b, []byte(`"[Imported from Cursor. `+cursorNote+`]\n\nRun the tests"`)) {
		t.Fatalf("interchange=%s", b)
	}
}

func TestEmptyCursorTranscriptIsNotImported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeCursor(t, path, cursorLine("assistant", "only a reply"))
	if _, err := LoadCursor(Session{Source: "cursor", Path: path}); err != ErrEmpty {
		t.Fatalf("err=%v", err)
	}
}

func TestCursorFolderIsFoundFromTheFilesystemRoot(t *testing.T) {
	want := filepath.Join(t.TempDir(), "My Project.v2")
	if err := os.MkdirAll(want, 0700); err != nil {
		t.Fatal(err)
	}
	name := cursorEncode(want)
	if got := CursorFolder(name, cursorRoots(name)); got != want {
		t.Fatalf("%s: got %q want %q", name, got, want)
	}
	if volume := filepath.VolumeName(want); volume != "" {
		root := volume + string(filepath.Separator)
		for _, invalid := range []string{"", "x", "wrong-drive-folder"} {
			if got := CursorFolder(invalid, []string{root}); got != "" {
				t.Fatalf("invalid drive prefix %q matched %q", invalid, got)
			}
		}
	}
}

func TestCursorNoteSurvivesWhenEarlyTurnsAreLeftOut(t *testing.T) {
	c := Conversation{Session: Session{Source: "cursor", ID: "long", Cwd: t.TempDir()}, Note: cursorNote}
	for n := 0; n < 40; n++ {
		c.Events = append(c.Events, Event{Kind: "user", Text: fmt.Sprintf("Question %d", n)}, Event{Kind: "assistant", Text: strings.Repeat("answer ", 2000)})
	}
	b, err := Interchange(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("earlier turns are left out so the conversation fits Copilot's context. "+cursorNote+"]")) || bytes.Contains(b, []byte(`"Question 0"`)) {
		t.Fatalf("the note must open the first kept turn: %.600s", b)
	}
}

func TestLooseConversationGetsNoRepository(t *testing.T) {
	home := t.TempDir()
	for name, text := range map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/config": "[remote \"origin\"]\n\turl = https://github.com/someone/dotfiles.git\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(home, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, loose := range []bool{false, true} {
		c := Conversation{Session: Session{Source: "cursor", ID: "x", Cwd: home, Loose: loose}, Events: []Event{{Kind: "user", Text: "Hi"}}}
		b, err := Interchange(c)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("dotfiles")) != !loose {
			t.Fatalf("loose=%v: %s", loose, b)
		}
	}
}

func TestCursorEnvelopeTagsTypedByTheUserAreKept(t *testing.T) {
	for in, want := range map[string]string{
		"Explain <user_query>hello</user_query> in this template":                              "Explain <user_query>hello</user_query> in this template",
		"<user_query>\nWrap <user_query>inner</user_query> done\n</user_query>":                "Wrap <user_query>inner</user_query> done",
		"Use <timestamp>now</timestamp> here":                                                  "Use <timestamp>now</timestamp> here",
		"<timestamp>Monday</timestamp>\n<user_query>Fix <timestamp>x</timestamp></user_query>": "Fix <timestamp>x</timestamp>",
		"<cursor_commands>/review</cursor_commands>\nRun it\n<timestamp>Monday</timestamp>":    "Run it",
	} {
		if got := cursorUserText([]cursorBlock{{Type: "text", Text: in}}); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
