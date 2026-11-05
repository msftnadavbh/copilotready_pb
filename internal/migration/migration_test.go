package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"copilotready/internal/sessions"
)

func fixture(t *testing.T) Options {
	t.Helper()
	for _, k := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "COPILOT_HOME"} {
		t.Setenv(k, "")
	}
	h := t.TempDir()
	return Options{Home: h, Destination: filepath.Join(h, "new-profile"), Mode: "new"}
}

func TestHookMigrationKeepsEntriesInTheirOriginalFormat(t *testing.T) {
	// Copilot runs its own and Claude-style entries natively, so matchers, platform
	// commands, cwd, env and timeouts transfer unchanged instead of being dropped.
	for _, tc := range []struct{ name, entry string }{
		{"platform", `{"type":"command","bash":"unix-hook","powershell":"windows-hook","command":"fallback-hook"}`},
		{"matcher", `{"matcher":"startup","hooks":[{"type":"command","command":"hook"}]}`},
		{"cwd", `{"type":"command","command":"hook","cwd":"other"}`},
		{"env", `{"type":"command","command":"hook","env":{"X":"Y"}}`},
		{"timeout", `{"type":"command","command":"hook","timeoutSec":30}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := fixture(t)
			o.Project = filepath.Join(o.Home, "project")
			put(t, filepath.Join(o.Home, ".copilot/hooks/example.json"), `{"version":1,"hooks":{"SessionStart":[`+tc.entry+`]}}`)
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			i := byName(t, r, "hooks", "SessionStart")
			if !i.Selectable {
				t.Fatalf("detail=%s", i.Detail)
			}
			a, err := GetActivation(r, i.ID)
			if err != nil {
				t.Fatal(err)
			}
			var want any
			_ = json.Unmarshal([]byte("["+tc.entry+"]"), &want)
			got, _ := json.Marshal(a.Config["entries"])
			wantJSON, _ := json.Marshal(want)
			if string(got) != string(wantJSON) || a.HookEvent != "SessionStart" || a.HookCommand != "" {
				t.Fatalf("entries=%s want %s", got, wantJSON)
			}
		})
	}
}

func TestClaudeAndCodexHooksTransferForAllCopilotEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Claude and Codex hook commands need a PowerShell adaptation on Windows")
	}
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/settings.json"), `{"hooks":{
		"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh","timeout":5}]}],
		"UserPromptSubmit":[{"hooks":[{"type":"command","command":"context.sh"}]}],
		"InstructionsLoaded":[{"hooks":[{"type":"command","command":"x.sh"}]}],
		"Stop":[{"hooks":[{"type":"command","command":"notify --token=abcdefghijk"}]}]}}`)
	put(t, filepath.Join(o.Home, ".codex/hooks.json"), `{"hooks":{"PostToolUse":[{"matcher":"shell","hooks":[{"type":"command","command":"log.sh"}]}]}}`)
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		source, event string
		selectable    bool
	}{{"claude", "PreToolUse", true}, {"claude", "UserPromptSubmit", true}, {"claude", "InstructionsLoaded", false}, {"claude", "Stop", false}, {"codex", "PostToolUse", true}} {
		var item Item
		for _, i := range r.Items {
			if i.Kind == "hooks" && i.Source == tc.source && i.Name == tc.event {
				item = i
			}
		}
		if item.ID == "" || item.Selectable != tc.selectable {
			t.Fatalf("%s %s selectable=%v detail=%s", tc.source, tc.event, item.Selectable, item.Detail)
		}
		if !tc.selectable {
			continue
		}
		a, err := GetActivation(r, item.ID)
		if err != nil {
			t.Fatal(err)
		}
		handlers := HookHandlers(a.Config["entries"].([]any))
		if len(handlers) != 1 || a.HookEvent != tc.event || a.HookSource != tc.source {
			t.Fatalf("%+v", a)
		}
		if ContextHookEvent(tc.source, tc.event) != (tc.event == "UserPromptSubmit") {
			t.Fatalf("%s context adapter choice", tc.event)
		}
	}
	pre := byName(t, r, "hooks", "PreToolUse")
	a, _ := GetActivation(r, pre.ID)
	entry := a.Config["entries"].([]any)[0].(map[string]any)
	if entry["matcher"] != "Bash" || HookHandlers(a.Config["entries"].([]any))[0]["timeout"] != float64(5) {
		t.Fatalf("matcher or timeout changed: %+v", entry)
	}
}

func TestPluginActivationRecordsTheSourceMarketplace(t *testing.T) {
	o := fixture(t)
	local := filepath.Join(o.Home, "markets", "team")
	put(t, filepath.Join(local, ".claude-plugin/marketplace.json"), `{"name":"team","plugins":[]}`)
	put(t, filepath.Join(o.Home, ".claude/plugins/known_marketplaces.json"), `{"claude-plugins-official":{"source":{"source":"github","repo":"anthropics/claude-plugins-official"}},"team":{"source":{"source":"directory","path":"`+filepath.ToSlash(local)+`"}},"hosted":{"source":{"source":"url","url":"https://example.com/marketplace.json"}}}`)
	put(t, filepath.Join(o.Home, ".claude/plugins/installed_plugins.json"), `{"version":2,"plugins":{"superpowers@claude-plugins-official":[{}],"helper@team":[{}],"site@hosted":[{}],"tool@awesome-copilot":[{}]}}`)
	bundled := filepath.Join(o.Home, ".codex/.tmp/bundled-marketplaces/openai-bundled")
	put(t, filepath.Join(bundled, "marketplace.json"), `{}`)
	put(t, filepath.Join(o.Home, ".codex/config.toml"), "[marketplaces.aws]\nsource_type = \"git\"\nsource = \"https://github.com/aws/agent-toolkit-for-aws.git\"\n"+
		"[marketplaces.openai-bundled]\nsource_type = \"local\"\nsource = \""+filepath.ToSlash(bundled)+"\"\n"+
		"[plugins.\"core@aws\"]\nenabled = true\n[plugins.\"browser@openai-bundled\"]\nenabled = true\n[plugins.\"github@openai-curated\"]\nenabled = true\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"superpowers@claude-plugins-official": "anthropics/claude-plugins-official", "helper@team": local, "core@aws": "https://github.com/aws/agent-toolkit-for-aws.git", "tool@awesome-copilot": ""} {
		i := byName(t, r, "plugin", name)
		a, err := GetActivation(r, i.ID)
		if err != nil || !i.Selectable || a.MarketplaceSource != want {
			t.Fatalf("%s: source=%q want %q (%v) %s", name, a.MarketplaceSource, want, err, i.Detail)
		}
	}
	for _, name := range []string{"site@hosted", "browser@openai-bundled", "github@openai-curated"} {
		if i := byName(t, r, "plugin", name); i.Selectable {
			t.Fatalf("%s should need review: %s", name, i.Detail)
		}
	}
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
func byName(t *testing.T, r Report, kind, name string) Item {
	t.Helper()
	for _, i := range r.Items {
		if i.Kind == kind && i.Name == name {
			return i
		}
	}
	t.Fatalf("missing %s %s: %+v", kind, name, r.Items)
	return Item{}
}

func TestCodexMCPTranslationNeverCopiesSecretsOrRuntimeNodeRepl(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".codex/config.toml"), `model = "unmapped-model"
 [mcp_servers.docs]
 url = "https://example.com/mcp"
 [mcp_servers.local]
 command = "node"
 args = ["server.js"]
 [mcp_servers.local.env]
 API_TOKEN = "secret-value-DO-NOT-COPY"
 [mcp_servers.node_repl]
 command = "node"
 args = ["codex-runtime.js"]
 `)
	r, e := Discover(o)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "secret-value") {
		t.Fatal("secret leaked into report")
	}
	n := byName(t, r, "mcp", "node_repl")
	if n.Selectable || n.Disposition != "unsupported" {
		t.Fatalf("runtime tool selectable: %+v", n)
	}
	d := byName(t, r, "mcp", "docs")
	if !d.Selectable {
		t.Fatal(d.Detail)
	}
	changes, e := BuildChanges(r, []string{d.ID}, nil)
	if e != nil {
		t.Fatal(e)
	}
	// An approved server goes straight into Copilot's configuration with every tool enabled.
	if len(changes) != 1 || filepath.Base(changes[0].Path) != "mcp-config.json" {
		t.Fatalf("approved MCP server not written to Copilot's configuration: %+v", changes)
	}
	if !strings.Contains(string(changes[0].Content), `"type": "http"`) || strings.Contains(string(changes[0].Content), `"tools"`) {
		t.Fatalf("HTTP server not translated with all tools: %s", changes[0].Content)
	}
	l := byName(t, r, "mcp", "local")
	if l.Selectable {
		t.Fatal("literal credentials must block import until edited")
	}
}

func TestPortableSkillCopiesSupportingFilesAndDetectsSourceChange(t *testing.T) {
	o := fixture(t)
	root := filepath.Join(o.Home, ".claude/skills/review")
	put(t, filepath.Join(root, "SKILL.md"), "---\nname: review\ndescription: Review source changes\n---\nRead reference.txt.\n")
	put(t, filepath.Join(root, "reference.txt"), "Review actual diff.")
	r, e := Discover(o)
	if e != nil {
		t.Fatal(e)
	}
	i := byName(t, r, "skill", "review")
	var supportingPreview bool
	for _, file := range i.Files {
		if file.Path == "reference.txt" && file.Size == int64(len("Review actual diff.")) && file.Text && strings.Contains(file.Preview, "Review actual diff.") {
			supportingPreview = true
		}
	}
	if !supportingPreview {
		t.Fatal("supporting file missing from review")
	}
	c, e := BuildChanges(r, []string{i.ID}, nil)
	if e != nil || len(c) != 2 {
		t.Fatalf("%+v %v", c, e)
	}
	for _, v := range c {
		if !strings.Contains(v.Path, filepath.Join("skills", "review")) {
			t.Fatal(v.Path)
		}
	}
	put(t, filepath.Join(root, "reference.txt"), "Changed after review.")
	if _, e = BuildChanges(r, []string{i.ID}, nil); e == nil {
		t.Fatal("source race ignored")
	}
}

func TestCompetingImportsStayManualAndReadySetBuilds(t *testing.T) {
	o := fixture(t)
	skill := "---\nname: review\ndescription: Review changes\n---\nRead reference.txt.\n"
	for _, source := range []string{"codex", "claude"} {
		root := filepath.Join(o.Home, "."+source, "skills", "review")
		put(t, filepath.Join(root, "0-"+source+".txt"), "Unique first destination")
		put(t, filepath.Join(root, "SKILL.md"), skill)
		put(t, filepath.Join(root, "reference.txt"), source+" review instructions")
	}
	earlier := time.Now().Add(-time.Hour)
	for _, file := range []string{"0-codex.txt", "SKILL.md", "reference.txt"} {
		if err := os.Chtimes(filepath.Join(o.Home, ".codex/skills/review", file), earlier, earlier); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(o.Home, ".codex/agents/Review.md"), "---\ndescription: Review changes\n---\nCodex agent")
	put(t, filepath.Join(o.Home, ".claude/agents/review.md"), "---\ndescription: Review changes\n---\nClaude agent")
	shared := "---\nname: shared-review\ndescription: Shared review\n---\nReview changes.\n"
	put(t, filepath.Join(o.Home, ".agents/skills/shared-review/SKILL.md"), shared)
	put(t, filepath.Join(o.Home, ".claude/skills/shared-review/SKILL.md"), shared)
	put(t, filepath.Join(o.Home, ".claude/skills/unique/SKILL.md"), "---\nname: unique\ndescription: Unique skill\n---\nText")
	put(t, filepath.Join(o.Home, ".codex/AGENTS.md"), "Codex instructions")
	put(t, filepath.Join(o.Home, ".claude/CLAUDE.md"), "Claude instructions")
	put(t, filepath.Join(o.Home, ".copilot/settings.json"), `{"theme":"dark"}`)
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	var ready, duplicateSkills []string
	manual, native := 0, 0
	for _, item := range r.Items {
		if item.Disposition == "native" {
			native++
			if item.Selectable {
				t.Fatal("native item became an import")
			}
			continue
		}
		if item.Kind == "skill" && item.Name == "review" {
			// Differing copies of one skill: the more recently edited one (Claude's, written
			// last) is brought over and the other stays a selectable alternative.
			duplicateSkills = append(duplicateSkills, item.ID)
			if want := map[string]string{"claude": "convertible", "codex": "needs-review"}[item.Source]; item.Disposition != want || !item.Selectable {
				t.Fatalf("%s copy of a same-name skill: %+v", item.Source, item)
			}
		}
		if item.Kind == "agent" || item.Kind == "skill" && item.Name == "shared-review" {
			manual++
			if !item.Selectable || item.Conflict || item.Disposition != "needs-review" {
				t.Fatalf("alternative must remain individually selectable without overwrite approval: %+v", item)
			}
			if _, err := BuildChanges(r, []string{item.ID}, nil); err != nil {
				t.Fatalf("individual alternative: %v", err)
			}
		}
		if item.Selectable && !item.Conflict && item.Disposition == "convertible" {
			ready = append(ready, item.ID)
		}
	}
	if manual != 3 || native != 1 || len(ready) != 5 || len(duplicateSkills) != 2 {
		t.Fatalf("manual=%d native=%d ready=%d; expected the newer review skill, unique skill, two mergeable instruction imports and settings ready", manual, native, len(ready))
	}
	if _, err := BuildChanges(r, ready, nil); err != nil {
		t.Fatalf("default ready selection must build: %v", err)
	}
	if _, err := BuildChanges(r, duplicateSkills, nil); err == nil {
		t.Fatal("explicitly selecting both differing skills must still require choosing one")
	}
}

func TestTargetConflictNeedsExplicitSelectionAndKeepsSnapshot(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/skills/review/SKILL.md"), "---\nname: review\ndescription: New review\n---\nNew")
	dest := filepath.Join(o.Destination, "skills/review/SKILL.md")
	put(t, dest, "original")
	r, e := Discover(o)
	if e != nil {
		t.Fatal(e)
	}
	i := byName(t, r, "skill", "review")
	if !i.Conflict {
		t.Fatal("conflict not shown")
	}
	if _, e = BuildChanges(r, []string{i.ID}, nil); e == nil {
		t.Fatal("unapproved overwrite")
	}
	c, e := BuildChanges(r, []string{i.ID}, []string{i.ID})
	if e != nil || len(c) != 1 {
		t.Fatal(e)
	}
	if c[0].BeforeSHA256 == "absent" {
		t.Fatal("missing target concurrency guard")
	}
	put(t, dest, "edited after review")
	if _, e = BuildChanges(r, []string{i.ID}, []string{i.ID}); e == nil {
		t.Fatal("target race ignored")
	}
}

func TestExistingProfileIsNotDuplicatedAndProjectInstructionsStayNative(t *testing.T) {
	o := fixture(t)
	o.Mode = "existing"
	o.Destination = filepath.Join(o.Home, ".copilot")
	repo := filepath.Join(o.Home, "project")
	o.Project = filepath.Join(repo, "packages", "app")
	put(t, filepath.Join(o.Destination, "skills/review/SKILL.md"), "---\nname: review\ndescription: Review\n---\nText")
	put(t, filepath.Join(repo, "CLAUDE.md"), "Project guidance")
	put(t, filepath.Join(repo, ".git"), "gitdir: ignored-for-this-fixture")
	put(t, filepath.Join(repo, ".mcp.json"), `{"root-docs":{"url":"https://example.com/mcp"}}`)
	put(t, filepath.Join(o.Home, ".mcp.json"), `{"outside-repo":{"url":"https://example.com/mcp"}}`)
	r, e := Discover(o)
	if e != nil {
		t.Fatal(e)
	}
	i := byName(t, r, "skill", "review")
	if i.Selectable || i.Disposition != "native" {
		t.Fatal(i)
	}
	p := byName(t, r, "instructions", "CLAUDE.md")
	if p.Selectable || p.Disposition != "native" {
		t.Fatal(p)
	}
	mcp := byName(t, r, "mcp", "root-docs")
	if mcp.Selectable || mcp.Disposition != "native" {
		t.Fatal(mcp)
	}
	for _, item := range r.Items {
		if item.Name == "outside-repo" {
			t.Fatal("inventory escaped nearest repository root")
		}
	}
}

func TestInstructionsPreserveRelativeIncludesAndSettingsComments(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".codex/AGENTS.md"), "Use @notes/style.md\n")
	put(t, filepath.Join(o.Home, ".codex/notes/style.md"), "Small changes.")
	put(t, filepath.Join(o.Home, ".copilot/settings.json"), "{\"theme\":\"dark\",\"model\":\"auto\"}")
	put(t, filepath.Join(o.Destination, "settings.json"), "{\n // keep this note\n \"unknownUserKey\":42\n}\n")
	r, e := Discover(o)
	if e != nil {
		t.Fatal(e)
	}
	i := byName(t, r, "instructions", "AGENTS.md")
	s := byName(t, r, "settings", "Copilot preferences")
	c, e := BuildChanges(r, []string{i.ID, s.ID}, []string{s.ID})
	if e != nil {
		t.Fatal(e)
	}
	all := ""
	for _, v := range c {
		all += string(v.Content)
		if strings.HasSuffix(v.Path, "settings.json") && (!strings.Contains(string(v.Content), "keep this note") || !strings.Contains(string(v.Content), "unknownUserKey")) {
			t.Fatal("settings comment/key lost")
		}
	}
	if !strings.Contains(all, "Small changes.") || !strings.Contains(all, "@imports/codex/AGENTS.md") {
		t.Fatal("relative instructions not carried")
	}
}

func TestDangerousCommandAndUnportableHookStayUnsupported(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/commands/release.md"), "Deploy !`cat .env` $ARGUMENTS")
	put(t, filepath.Join(o.Home, ".codex/config.toml"), "[hooks]\nSessionStart = [{command='codex-only-hook'}]\n")
	r, e := Discover(o)
	if e != nil {
		t.Fatal(e)
	}
	i := byName(t, r, "prompt", "release")
	if i.Selectable {
		t.Fatal("dynamic command silently converted")
	}
	raw, _ := json.Marshal(r)
	if !strings.Contains(string(raw), "hooks") {
		t.Fatal("hooks not inventoried")
	}
}

func TestContextAdapterPreservesContextAndRejectsBehavioralOutput(t *testing.T) {
	for _, source := range []string{"codex", "claude"} {
		for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
			b, e := AdaptContextOutput(source, event, []byte(`{"hookSpecificOutput":{"hookEventName":"`+event+`","additionalContext":"Use small changes."}}`))
			if e != nil || !strings.Contains(string(b), `"additionalContext": "Use small changes."`) {
				t.Fatalf("%s %s: %s %v", source, event, b, e)
			}
			if b, e = AdaptContextOutput(source, event, []byte("Plain stdout is context.\n")); e != nil || !strings.Contains(string(b), `"additionalContext": "Plain stdout is context."`) {
				t.Fatalf("%s %s plain: %s %v", source, event, b, e)
			}
			if _, e = AdaptContextOutput(source, event, []byte(`{"hookSpecificOutput":{"additionalContext":"x","permissionDecision":"allow"}}`)); e == nil {
				t.Fatal("behavioral output silently discarded")
			}
		}
		if _, e := AdaptContextOutput(source, "UserPromptSubmit", []byte(`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"x"}}`)); e == nil {
			t.Fatal("output for another event accepted")
		}
	}
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude.json"), `{"mcpServers":{"docs":{"url":"https://example.com/mcp"}}}`)
	r, e := Discover(o)
	if e != nil {
		t.Fatal(e)
	}
	if !byName(t, r, "mcp", "docs").Selectable {
		t.Fatal("global Claude config missed without directory")
	}
}

func TestContextAdapterTranslatesPromptBlocksAndPlainBraces(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"decision":"block","reason":"No secrets in prompts."}`, `"decision": "block"`},
		{`{"continue":false,"stopReason":"Paused."}`, `"reason": "Paused."`},
		{`{project}: use small changes`, `"additionalContext": "{project}: use small changes"`},
	} {
		b, e := AdaptContextOutput("claude", "UserPromptSubmit", []byte(tc.raw))
		if e != nil || !strings.Contains(string(b), tc.want) {
			t.Fatalf("%s -> %s %v", tc.raw, b, e)
		}
	}
	if _, e := AdaptContextOutput("claude", "SessionStart", []byte(`{"decision":"block"}`)); e == nil {
		t.Fatal("SessionStart cannot block a prompt")
	}
	if _, e := AdaptContextOutput("codex", "UserPromptSubmit", []byte(`{"decision":"approve"}`)); e == nil {
		t.Fatal("unknown decision accepted")
	}
}

func TestHookReviewCatchesCredentialsAndUnrunnableCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Claude hook commands need a PowerShell adaptation on Windows")
	}
	for _, tc := range []struct {
		name, handler string
		selectable    bool
	}{
		{"quoted env credential", `{"type":"command","command":"guard.sh","env":{"API_KEY":"example-value-123"}}`, false},
		{"env reference", `{"type":"command","command":"guard.sh","env":{"API_KEY":"${API_KEY}"}}`, true},
		{"other platform only", `{"type":"command","powershell":"guard.ps1"}`, false},
		{"exec only context", `{"type":"command","exec":"/usr/bin/true"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := fixture(t)
			put(t, filepath.Join(o.Home, ".claude/settings.json"), `{"hooks":{"UserPromptSubmit":[{"hooks":[`+tc.handler+`]}]}}`)
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			if i := byName(t, r, "hooks", "UserPromptSubmit"); i.Selectable != tc.selectable {
				t.Fatalf("selectable=%v detail=%s", i.Selectable, i.Detail)
			}
		})
	}
}

func TestMarketplaceSourcesRejectCredentialsButKeepUserFolders(t *testing.T) {
	o := fixture(t)
	team := filepath.Join(o.Home, ".codex/markets/team")
	put(t, filepath.Join(team, "marketplace.json"), `{}`)
	put(t, filepath.Join(o.Home, ".codex/config.toml"), "[marketplaces.team]\nsource_type = \"local\"\nsource = \""+filepath.ToSlash(team)+"\"\n"+
		"[marketplaces.userinfo]\nsource_type = \"git\"\nsource = \"https://someone:hunter2hunter2@example.com/m.git\"\n"+
		"[marketplaces.token]\nsource_type = \"git\"\nsource = \"https://ghp_abcdefghijklmnop1234@github.com/org/m.git\"\n"+
		"[marketplaces.ssh]\nsource_type = \"git\"\nsource = \"ssh://git@github.com/org/m.git\"\n"+
		"[plugins.\"a@team\"]\nenabled = true\n[plugins.\"b@userinfo\"]\nenabled = true\n[plugins.\"c@token\"]\nenabled = true\n[plugins.\"d@ssh\"]\nenabled = true\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for name, selectable := range map[string]bool{"a@team": true, "b@userinfo": false, "c@token": false, "d@ssh": true} {
		i := byName(t, r, "plugin", name)
		if i.Selectable != selectable {
			t.Fatalf("%s selectable=%v detail=%s", name, i.Selectable, i.Detail)
		}
		if a, _ := GetActivation(r, i.ID); !selectable && a.MarketplaceSource != "" {
			t.Fatalf("%s staged a credential-bearing source", name)
		}
	}
}

func TestConversationsAreOfferedOnceAndSkippedAfterImport(t *testing.T) {
	o := fixture(t)
	o.SessionRecord = filepath.Join(o.Home, "imported-sessions.json")
	work := filepath.Join(o.Home, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	transcript := func(id, cwd string) {
		line, _ := json.Marshal(map[string]any{"type": "user", "uuid": "u1", "cwd": cwd, "timestamp": "2026-10-01T10:00:00Z", "message": map[string]any{"role": "user", "content": "Fix the build"}})
		put(t, filepath.Join(o.Home, ".claude", "projects", "p", id+".jsonl"), string(line)+"\n")
	}
	const ready = "11111111-1111-1111-1111-111111111111"
	transcript(ready, work)
	transcript("22222222-2222-2222-2222-222222222222", filepath.Join(o.Home, "deleted"))
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	i := byName(t, r, "conversations", "1 conversation")
	if !i.Selectable || i.Disposition != "convertible" || !strings.Contains(i.Preview, "Fix the build") {
		t.Fatalf("item=%+v", i)
	}
	if c := Conversations(r, i.ID); len(c) != 1 || c[0].ID != ready {
		t.Fatalf("conversations=%+v", c)
	}
	if m := byName(t, r, "conversations", "1 conversation from missing folders"); m.Selectable {
		t.Fatal("a conversation without its folder cannot be imported")
	}
	if changes, err := BuildChanges(r, []string{i.ID}, nil); err != nil || len(changes) != 0 {
		t.Fatalf("conversations write no files during Apply: %v %v", changes, err)
	}
	if err = sessions.OpenRecord(o.SessionRecord).Add(o.Destination, "claude:"+ready, "copilot-session"); err != nil {
		t.Fatal(err)
	}
	if r, err = Discover(o); err != nil {
		t.Fatal(err)
	}
	if again := byName(t, r, "conversations", "1 conversation"); again.Selectable || again.Disposition != "native" {
		t.Fatalf("already imported conversation offered again: %+v", again)
	}
}

func TestInstructionImportKeepsExistingInstructionsWithoutReplaceWarning(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude", "CLAUDE.md"), "Prefer small diffs.")
	existing := filepath.Join(o.Destination, "copilot-instructions.md")
	put(t, existing, "My own instructions.")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	i := byName(t, r, "instructions", "CLAUDE.md")
	if i.Conflict || !i.Selectable || !strings.Contains(i.Detail, "is kept") {
		t.Fatalf("item=%+v", i)
	}
	changes, err := BuildChanges(r, []string{i.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Path == existing && (!strings.HasPrefix(string(c.Content), "My own instructions.") || !strings.Contains(string(c.Content), "@imports/claude/CLAUDE.md")) {
			t.Fatalf("instructions=%q", c.Content)
		}
	}
}

func TestCodexBundledPluginsWithExtendedWindowsPathsNeedWork(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip(`the Codex app writes \?\ paths only on Windows`)
	}
	o := fixture(t)
	bundled := filepath.Join(o.Home, ".codex", ".tmp", "bundled-marketplaces", "openai-bundled")
	put(t, filepath.Join(bundled, ".agents", "plugins", "marketplace.json"), `{"name":"openai-bundled","plugins":[]}`)
	put(t, filepath.Join(o.Home, ".codex", "config.toml"), "[marketplaces.openai-bundled]\nsource_type = \"local\"\nsource = '"+`\\?\`+bundled+"'\n[plugins.\"browser@openai-bundled\"]\nenabled = true\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if i := byName(t, r, "plugin", "browser@openai-bundled"); i.Selectable || !strings.Contains(i.Detail, "bundled with the Codex app") {
		t.Fatalf("bundled plugin offered as ready: %+v", i)
	}
}

func TestRecordedFoldersBecomePlainPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("extended and UNC paths are Windows forms")
	}
	for in, want := range map[string]string{
		`\\?\C:\Users\me\market`:      `C:\Users\me\market`,
		`\\?\UNC\server\share\market`: `\\server\share\market`,
		`C:/Users/me/market/`:         `C:\Users\me\market`,
		`\\server\share\market`:       `\\server\share\market`,
	} {
		if got := nativePath(in); got != want || !filepath.IsAbs(got) {
			t.Errorf("nativePath(%q) = %q, want absolute %q", in, got, want)
		}
	}
}

func TestApprovedMCPServersGoIntoCopilotConfigWithAllTools(t *testing.T) {
	o := fixture(t)
	o.Mode = "existing"
	o.Destination = filepath.Join(o.Home, ".copilot")
	o.CopilotHome = o.Destination
	config := filepath.Join(o.Destination, "mcp-config.json")
	put(t, config, `{
  // mine
  "mcpServers": {
    "github": {"type": "http", "url": "https://api.example/mcp", "tools": ["*"]},
    "signed-in": {"type": "http", "url": "https://auth.example/mcp", "tools": []}
  }
}`)
	put(t, filepath.Join(o.Home, ".claude.json"), `{"mcpServers":{"docs":{"url":"https://docs.example/mcp"},"github":{"url":"https://other.example/mcp"}}}`)
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	docs, repair := byName(t, r, "mcp", "docs"), Item{}
	for _, i := range r.Items {
		if i.Kind == "mcp" && i.Name == "signed-in" && i.Source == "copilot" {
			repair = i
		}
		if i.Kind == "mcp" && i.Name == "github" && i.Source == "claude" && i.Disposition == "convertible" {
			t.Fatal("a server with an existing name must not be ready to import")
		}
	}
	if docs.Disposition != "convertible" || docs.Conflict || repair.Disposition != "convertible" || !repair.Selectable {
		t.Fatalf("docs=%+v repair=%+v", docs, repair)
	}
	changes, err := BuildChanges(r, []string{docs.ID, repair.ID}, nil)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes=%v err=%v", changes, err)
	}
	got := string(changes[0].Content)
	for _, want := range []string{"// mine", `"github"`, `"docs"`, "https://docs.example/mcp"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	var parsed struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if v, err := parseJSON([]byte(got)); err != nil {
		t.Fatal(err)
	} else {
		b, _ := json.Marshal(v)
		_ = json.Unmarshal(b, &parsed)
	}
	if _, hasTools := parsed.MCPServers["docs"]["tools"]; hasTools || fmt.Sprint(parsed.MCPServers["signed-in"]["tools"]) != "[*]" {
		t.Fatalf("tools: docs=%v signed-in=%v", parsed.MCPServers["docs"], parsed.MCPServers["signed-in"])
	}
}

func TestPluginsTurnedOffInTheSourceAreNotPreselected(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude", "settings.json"), `{"enabledPlugins":{"on@awesome-copilot":true,"off@awesome-copilot":false,"listed@awesome-copilot":false}}`)
	put(t, filepath.Join(o.Home, ".claude", "plugins", "installed_plugins.json"), `{"version":2,"plugins":{"listed@awesome-copilot":[{}]}}`)
	put(t, filepath.Join(o.Home, ".codex", "config.toml"), "[plugins.\"codex-off@awesome-copilot\"]\nenabled = false\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range r.Items {
		if i.Kind != "plugin" {
			continue
		}
		ready := i.Selectable && i.Disposition == "convertible"
		if want := i.Name == "on@awesome-copilot"; ready != want {
			t.Fatalf("%s from %s: ready=%v detail=%s", i.Name, i.Source, ready, i.Detail)
		}
		if i.Name != "on@awesome-copilot" && !strings.Contains(i.Detail, "turned off") {
			t.Fatalf("%s: %s", i.Name, i.Detail)
		}
	}
}

func TestTwoSelectedServersWithOneNameAreRefused(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude.json"), `{"mcpServers":{"docs":{"url":"https://claude.example/mcp"}}}`)
	put(t, filepath.Join(o.Home, ".codex", "config.toml"), "[mcp_servers.docs]\nurl = \"https://codex.example/mcp\"\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range r.Items {
		if i.Kind == "mcp" && i.Name == "docs" {
			if i.Disposition == "convertible" {
				t.Fatalf("same-named server offered as ready: %+v", i)
			}
			ids = append(ids, i.ID)
		}
	}
	if _, err = BuildChanges(r, ids, nil); len(ids) != 2 || err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("ids=%d err=%v", len(ids), err)
	}
}
