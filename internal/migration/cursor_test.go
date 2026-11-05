package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cursorFixture is a fixture with Cursor turned on.
func cursorFixture(t *testing.T) Options {
	o := fixture(t)
	o.Cursor = true
	return o
}

func cursorItem(t *testing.T, r Report, source, kind, name string) Item {
	t.Helper()
	for _, i := range r.Items {
		if i.Source == source && i.Kind == kind && i.Name == name {
			return i
		}
	}
	t.Fatalf("missing %s %s %s: %+v", source, kind, name, r.Items)
	return Item{}
}

func TestCursorRulesAreShownAsCopilotInstructions(t *testing.T) {
	o := cursorFixture(t)
	o.Project = filepath.Join(o.Home, "project")
	rules := filepath.Join(o.Project, ".cursor", "rules")
	put(t, filepath.Join(rules, "always.mdc"), "---\ndescription: House style\nglobs: *.go\nalwaysApply: true\n---\nUse tabs.\n")
	put(t, filepath.Join(rules, "typescript.mdc"), "---\ndescription: TS\nglobs: *.ts\nalwaysApply: false\n---\nStrict mode.\n")
	put(t, filepath.Join(rules, "web.mdc"), "---\nglobs: src/**/*.ts, \"*.tsx\"\n---\nReact.\n")
	put(t, filepath.Join(rules, "listed.mdc"), "---\nglobs:\n  - docs/**\n---\nDocs.\n")
	put(t, filepath.Join(rules, "described.mdc"), "---\ndescription: Database migrations\nglobs:\nalwaysApply: false\n---\nUse goose.\n")
	put(t, filepath.Join(rules, "manual.mdc"), "Only when asked.\n")
	put(t, filepath.Join(rules, "nested", "react.mdc"), "---\nglobs: **/*.jsx\n---\nHooks.\n")
	put(t, filepath.Join(rules, "broken.mdc"), "---\nglobs: *.ts\nthis is: [not: closed\n---\nBody\n")
	put(t, filepath.Join(rules, "notes.md"), "Cursor ignores plain Markdown here.\n")
	put(t, filepath.Join(rules, "quoted.mdc"), "---\ndescription: Quoted\nalwaysApply: \"true\"\n---\nAlways.\n")
	put(t, filepath.Join(o.Project, ".cursorrules"), "Prefer small functions.\n")
	put(t, filepath.Join(o.Project, "packages", "web", ".cursor", "rules", "react.mdc"), "---\nglobs: '**/*.{ts,tsx}'\n---\nHooks.\n")
	put(t, filepath.Join(o.Project, "packages", "web", ".cursor", "rules", "style.mdc"), "---\nalwaysApply: true\n---\nTabs.\n")
	put(t, filepath.Join(o.Project, "node_modules", "dep", ".cursor", "rules", "dep.mdc"), "---\nalwaysApply: true\n---\nNot ours.\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, apply, says string }{
		{"always.mdc", "applyTo: '**'", "every request"},
		{"typescript.mdc", "applyTo: '*.ts'", "matching files"},
		{"web.mdc", "applyTo: src/**/*.ts,*.tsx", "matching files"},
		{"listed.mdc", "applyTo: docs/**", "matching files"},
		{"nested/react.mdc", "applyTo: '**/*.jsx'", ".github/instructions/nested-react.instructions.md"},
		{".cursorrules", "applyTo: '**'", ".github/instructions/cursorrules.instructions.md"},
		{"quoted.mdc", "applyTo: '**'", "every request"},
		{"packages/web/.cursor/rules/react.mdc", "applyTo: packages/web/**/*.ts,packages/web/**/*.tsx", "only for files in packages/web"},
		{"packages/web/.cursor/rules/style.mdc", "applyTo: packages/web/**", ".github/instructions/packages-web-style.instructions.md"},
	} {
		i := cursorItem(t, r, "cursor-project", "rules", tc.name)
		if i.Selectable || i.Disposition != "needs-review" || !strings.Contains(i.Preview, tc.apply) || !strings.Contains(i.Detail, tc.says) || !strings.Contains(i.Detail, "no repository files will be changed") {
			t.Errorf("%s: %+v", tc.name, i)
		}
	}
	for name, says := range map[string]string{"described.mdc": "description fits", "manual.mdc": "@-mention", "broken.mdc": "can't be read"} {
		if i := cursorItem(t, r, "cursor-project", "rules", name); i.Selectable || strings.Contains(i.Preview, "applyTo") || !strings.Contains(i.Detail, says) {
			t.Errorf("%s: %+v", name, i)
		}
	}
	for _, i := range r.Items {
		if i.Name == "notes.md" || strings.Contains(i.Name, "dep.mdc") {
			t.Fatalf("Cursor reads only .mdc rules, and dependency folders are skipped: %+v", i)
		}
	}
	if changes, err := BuildChanges(r, nil, nil); err != nil || len(changes) != 0 {
		t.Fatalf("nothing is written into the repository: %v %v", changes, err)
	}
}

func TestCursorRuleHeaderReadsWhatYAMLRejects(t *testing.T) {
	for _, tc := range []struct {
		in     string
		desc   string
		globs  []string
		always bool
		ok     bool
	}{
		{"---\nglobs: **/*.ts\n---\nx", "", []string{"**/*.ts"}, false, true},
		{"---\r\ndescription: 'A: b'\r\nglobs: *.ts, *.tsx\r\nalwaysApply: true\r\n---\r\nx", "A: b", []string{"*.ts", "*.tsx"}, true, true},
		{"\ufeff---\nglobs: *.md\n# a comment\n\n---\nx", "", []string{"*.md"}, false, true},
		{"---\nglobs: src/*.{ts,tsx}, docs/[a,b].md, {x,y}/{1,2}\n---\nx", "", []string{"src/*.ts", "src/*.tsx", "docs/[a,b].md", "x/1", "x/2", "y/1", "y/2"}, false, true},
		{"---\nglobs: a/{b,c\n---\nx", "", []string{"a/{b,c"}, false, true},
		{"---\nglobs: file{1..3}.ts, src/{draft}/{a,b}\n---\nx", "", []string{"file{1..3}.ts", "src/{draft}/a", "src/{draft}/b"}, false, true},
		{"---\nglobs: " + strings.Repeat("{a,b}", 30) + "\n---\nx", "", []string{strings.Repeat("{a,b}", 30)}, false, true},
		{"---\nglobs: *.ts\nother: [\n---\nx", "", nil, false, false},
		{"---\nglobs: *.ts\n", "", nil, false, false},
		{"---", "", nil, false, true},
		{"plain text", "", nil, false, true},
	} {
		h, _, ok := cursorRuleHeader([]byte(tc.in))
		if ok != tc.ok || ok && (h.description != tc.desc || strings.Join(h.globs, "|") != strings.Join(tc.globs, "|") || h.always != tc.always) {
			t.Errorf("%q: %+v ok=%v", tc.in, h, ok)
		}
	}
}

func TestCursorMCPServersUseCopilotReferences(t *testing.T) {
	o := cursorFixture(t)
	t.Setenv("API_TOKEN", "set")
	command, _ := json.Marshal(os.Args[0])
	put(t, filepath.Join(o.Home, ".cursor", "mcp.json"), `{"mcpServers":{
		"remote":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${env:API_TOKEN}"}},
		"local":{"command":`+string(command)+`,"args":["--root","${userHome}${/}data"]},
		"project":{"command":`+string(command)+`,"args":["${workspaceFolder}"]},
		"fromfile":{"command":`+string(command)+`,"envFile":".env"},
		"signin":{"url":"https://example.com/oauth","auth":{"CLIENT_ID":"abc"}}}}`)
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	remote := cursorItem(t, r, "cursor", "mcp", "remote")
	if !remote.Selectable || remote.Disposition != "convertible" || !strings.Contains(remote.Preview, "Bearer ${API_TOKEN}") || strings.Contains(remote.Preview, "env:") {
		t.Fatalf("remote=%+v", remote)
	}
	local := cursorItem(t, r, "cursor", "mcp", "local")
	data, _ := json.Marshal(filepath.Join(o.Home, "data"))
	if !local.Selectable || !strings.Contains(local.Preview, string(data)) {
		t.Fatalf("local=%+v want %s", local, data)
	}
	for name, says := range map[string]string{"project": "${workspaceFolder}", "fromfile": "env files", "signin": "OAuth"} {
		if i := cursorItem(t, r, "cursor", "mcp", name); i.Selectable || !strings.Contains(i.Detail, says) {
			t.Errorf("%s: %+v", name, i)
		}
	}
}

func TestCursorSetupConvertsCommandsSkillsAndAgents(t *testing.T) {
	o := cursorFixture(t)
	cursor := filepath.Join(o.Home, ".cursor")
	put(t, filepath.Join(cursor, "commands", "review.md"), "Review the diff.\n")
	put(t, filepath.Join(cursor, "skills", "lint", "SKILL.md"), "---\nname: lint\ndescription: Lint the code\nicon: broom\ncolor: blue\npaths:\n  - src/**\n---\nRun the linter.\n")
	put(t, filepath.Join(cursor, "agents", "reader.md"), "---\nname: reader\ndescription: Reads code\nreadonly: true\n---\nRead only.\n")
	put(t, filepath.Join(cursor, "agents", "runner.md"), "---\nname: runner\ndescription: Runs tests\nis_background: true\n---\nRun tests.\n")
	put(t, filepath.Join(cursor, "hooks.json"), `{"version":1,"hooks":{"beforeSubmitPrompt":[{"command":"check.sh"}],"afterFileEdit":[{"command":"format.sh"}]}}`)
	put(t, filepath.Join(cursor, "cli-config.json"), `{"version":1,"permissions":{"allow":["Shell(ls)"]}}`)
	put(t, filepath.Join(cursor, "plugins", "cache", "x", "plugin.json"), `{}`)
	put(t, filepath.Join(o.Home, ".claude", "skills", "paint", "SKILL.md"), "---\nname: paint\ndescription: Paint\nicon: brush\n---\nx\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if i := cursorItem(t, r, "cursor", "prompt", "review"); !i.Selectable {
		t.Fatalf("command=%+v", i)
	}
	skill := cursorItem(t, r, "cursor", "skill", "lint")
	if !skill.Selectable || !strings.Contains(skill.Detail, "offers it everywhere") {
		t.Fatalf("skill=%+v", skill)
	}
	changes, err := BuildChanges(r, []string{skill.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var written string
	for _, c := range changes {
		if strings.HasSuffix(filepath.ToSlash(c.Path), "lint/SKILL.md") {
			written = string(c.Content)
		}
	}
	if !strings.Contains(written, "metadata:") || !strings.Contains(written, "icon: broom") || strings.Contains(written, "\nicon:") {
		t.Fatalf("Cursor-only keys belong under metadata: %s", written)
	}
	if i := cursorItem(t, r, "claude", "skill", "paint"); i.Selectable || !strings.Contains(i.Detail, "requires adaptation: icon") {
		t.Fatalf("only Cursor's icon, color and paths move under metadata: %+v", i)
	}
	if i := cursorItem(t, r, "cursor", "agent", "reader"); i.Selectable || !strings.Contains(i.Detail, "read-only in Cursor") {
		t.Fatalf("a read-only agent must not gain write access silently: %+v", i)
	}
	if i := cursorItem(t, r, "cursor", "agent", "runner"); !i.Selectable || !strings.Contains(i.Detail, "background") {
		t.Fatalf("runner=%+v", i)
	}
	if i := cursorItem(t, r, "cursor", "hooks", "beforeSubmitPrompt"); i.Selectable || !strings.Contains(i.Detail, "userPromptSubmitted") {
		t.Fatalf("hook=%+v", i)
	}
	if i := cursorItem(t, r, "cursor", "hooks", "afterFileEdit"); i.Selectable || !strings.Contains(i.Detail, "no afterFileEdit") {
		t.Fatalf("hook=%+v", i)
	}
	for _, kind := range [][2]string{{"instructions", "User Rules"}, {"permissions", "Cursor CLI permissions"}, {"plugin", "Cursor plugins"}} {
		if i := cursorItem(t, r, "cursor", kind[0], kind[1]); i.Selectable || i.Disposition != "unsupported" {
			t.Errorf("%v: %+v", kind, i)
		}
	}
}

func TestCursorProjectFilesAreListedWithoutChangingTheRepository(t *testing.T) {
	o := cursorFixture(t)
	o.Project = filepath.Join(o.Home, "project")
	dot := filepath.Join(o.Project, ".cursor")
	put(t, filepath.Join(dot, "mcp.json"), `{"mcpServers":{"docs":{"url":"https://example.com/mcp"}}}`)
	put(t, filepath.Join(dot, "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Deploy\n---\nx\n")
	put(t, filepath.Join(dot, "agents", "qa.md"), "---\nname: qa\ndescription: QA\n---\nx\n")
	put(t, filepath.Join(dot, "commands", "ship.md"), "Ship it.\n")
	put(t, filepath.Join(dot, "hooks.json"), `{"version":1,"hooks":{"stop":[{"command":"done.sh"}]}}`)
	put(t, filepath.Join(dot, "BUGBOT.md"), "Check SQL.\n")
	put(t, filepath.Join(o.Project, ".cursorignore"), "secrets/\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if i := cursorItem(t, r, "cursor-project", "mcp", "docs"); i.Selectable || !strings.Contains(i.Detail, "belongs to the selected project") {
		t.Fatalf("mcp=%+v", i)
	}
	if i := cursorItem(t, r, "cursor-project", "prompt", "ship"); !i.Selectable {
		t.Fatalf("project commands become personal skills, like Claude's: %+v", i)
	}
	for _, kind := range [][2]string{{"skill", ".cursor/skills"}, {"agent", ".cursor/agents"}, {"hooks", "stop"}, {"instructions", "BUGBOT.md"}, {"settings", ".cursorignore"}} {
		if i := cursorItem(t, r, "cursor-project", kind[0], kind[1]); i.Selectable {
			t.Errorf("%v: %+v", kind, i)
		}
	}
}

func TestCursorConversationsAreOfferedWithWhatTheyLack(t *testing.T) {
	o := cursorFixture(t)
	o.SessionRecord = filepath.Join(o.Home, "imported-sessions.json")
	work := filepath.Join(o.Home, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	transcript := func(folder, id, cwd string) {
		line, _ := json.Marshal(map[string]any{"role": "user", "cwd": cwd, "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "<user_query>\nFix the build\n</user_query>"}}}})
		put(t, filepath.Join(o.Home, ".cursor", "projects", folder, "agent-transcripts", id, id+".jsonl"), string(line)+"\n")
	}
	transcript("work", "ready", work)
	transcript("gone", "missing", filepath.Join(o.Home, "deleted"))
	transcript("empty-window", "nofolder", "")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	i := cursorItem(t, r, "cursor", "conversations", "2 conversations")
	if !i.Selectable || !strings.Contains(i.Preview, "Fix the build") || !strings.Contains(i.Detail, "doesn't record tool results") {
		t.Fatalf("item=%+v", i)
	}
	folders := map[string]string{}
	for _, c := range Conversations(r, i.ID) {
		folders[c.ID] = c.Cwd
	}
	if len(folders) != 2 || folders["ready"] != work || folders["nofolder"] != o.Home {
		t.Fatalf("conversations=%+v", folders)
	}
	if m := cursorItem(t, r, "cursor", "conversations", "1 conversation from missing folders"); m.Selectable || !strings.Contains(m.Detail, "shortened form") {
		t.Fatalf("missing=%+v", m)
	}
}

func TestSameNameCursorRulesInTheRepositoryAndProjectAreBothListed(t *testing.T) {
	o := cursorFixture(t)
	repo := filepath.Join(o.Home, "repo")
	o.Project = filepath.Join(repo, "app")
	put(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	put(t, filepath.Join(repo, ".cursorrules"), "Repository rule.\n")
	put(t, filepath.Join(o.Project, ".cursorrules"), "App rule.\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	var previews []string
	for _, i := range r.Items {
		if i.Source == "cursor-project" && i.Name == ".cursorrules" {
			previews = append(previews, i.Preview+i.Detail)
		}
	}
	if len(previews) != 2 || !strings.Contains(strings.Join(previews, ""), "Repository rule.") || !strings.Contains(strings.Join(previews, ""), "From "+filepath.Join(o.Project, ".cursorrules")) {
		t.Fatalf("rules=%v", previews)
	}
}

func TestCursorIsReadOnlyWhenTurnedOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		o := fixture(t)
		o.Cursor = on
		o.Project = filepath.Join(o.Home, "project")
		o.SessionRecord = filepath.Join(o.Home, "imported-sessions.json")
		put(t, filepath.Join(o.Home, ".cursor", "skills", "tidy", "SKILL.md"), "---\nname: tidy\ndescription: Tidy up\n---\nTidy.\n")
		put(t, filepath.Join(o.Home, ".cursor", "commands", "review.md"), "Review the diff.\n")
		put(t, filepath.Join(o.Project, ".cursor", "rules", "style.mdc"), "---\nalwaysApply: true\n---\nTabs.\n")
		put(t, filepath.Join(o.Project, "packages", "web", ".cursor", "rules", "react.mdc"), "---\nalwaysApply: true\n---\nHooks.\n")
		put(t, filepath.Join(o.Project, ".cursorrules"), "Small functions.\n")
		put(t, filepath.Join(o.Home, ".cursor", "projects", "empty-window", "agent-transcripts", "a.jsonl"), `{"role":"user","message":{"content":"Hello"}}`+"\n")
		r, err := Discover(o)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, i := range r.Items {
			if strings.HasPrefix(i.Source, "cursor") || strings.Contains(i.Detail, "Cursor") {
				found++
			}
		}
		for _, src := range r.Sources {
			if src.Name == "cursor" {
				found++
			}
		}
		if on != (found > 0) {
			t.Fatalf("cursor=%v: found %d Cursor rows: %+v %+v", on, found, r.Sources, r.Items)
		}
	}
}
