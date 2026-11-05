package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSkillCompatibilityPreservesFilesWithoutToolPreapproval(t *testing.T) {
	for _, source := range []string{"codex", "claude"} {
		t.Run(source, func(t *testing.T) {
			o := fixture(t)
			root := filepath.Join(o.Home, "."+source, "skills", "portable")
			put(t, filepath.Join(root, "SKILL.md"), "---\nname: portable\ndescription: Review a file\nallowed-tools: ['*']\nuser-invocable: true\ndisable-model-invocation: true\n---\nRead [reference](references/check.md). Run scripts/check.sh from this folder.\n")
			put(t, filepath.Join(root, "references/check.md"), "Review the actual input.\n")
			put(t, filepath.Join(root, "scripts/check.sh"), "#!/bin/sh\nprintf '%s\\n' checked\n")
			if err := os.Chmod(filepath.Join(root, "scripts/check.sh"), 0755); err != nil {
				t.Fatal(err)
			}
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			item := byName(t, r, "skill", "portable")
			if !item.Selectable || !strings.Contains(item.Detail, "pre-approvals are removed") {
				t.Fatal(item)
			}
			changes, err := BuildChanges(r, []string{item.ID}, nil)
			if err != nil || len(changes) != 3 {
				t.Fatalf("changes=%d err=%v", len(changes), err)
			}
			for _, change := range changes {
				if filepath.Base(change.Path) == "SKILL.md" {
					meta, _, err := frontmatter(change.Content)
					if err != nil || meta["allowed-tools"] != nil || meta["disable-model-invocation"] != true || meta["user-invocable"] != true {
						t.Fatalf("unsafe converted metadata: %v %v", meta, err)
					}
				}
				if filepath.Base(change.Path) == "check.sh" && runtime.GOOS != "windows" && change.Mode&0111 == 0 {
					t.Fatal("executable mode lost")
				}
			}
			if !previewFile("scripts/check.sh", []byte("#!/bin/sh\n"), 0755).Executable {
				t.Fatal("executable preview lost")
			}
			if !previewFile("scripts/check.ps1", []byte("Write-Output checked"), 0600).Executable {
				t.Fatal("Windows script not identified as code")
			}
			put(t, filepath.Join(o.Destination, "skills/portable/references/check.md"), "Existing reference")
			r, err = Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			item = byName(t, r, "skill", "portable")
			if !item.Conflict {
				t.Fatal("supporting-file replacement was not surfaced")
			}
			if _, err := BuildChanges(r, []string{item.ID}, nil); err == nil {
				t.Fatal("replacement did not require approval")
			}
		})
	}
}

func TestSkillCompatibilityBlocksSecretsWithoutPreviewingThem(t *testing.T) {
	for _, source := range []string{"codex", "claude"} {
		for _, file := range []string{"auth.json", "credentials.json", ".credentials.json", ".env", "settings.json"} {
			t.Run(source+"/"+file, func(t *testing.T) {
				o := fixture(t)
				root := filepath.Join(o.Home, "."+source, "skills", "private")
				put(t, filepath.Join(root, "SKILL.md"), "---\nname: private\ndescription: Local skill\n---\nText\n")
				value := "opaque-sensitive-value-no-known-token-format"
				content := value
				if file == "settings.json" {
					content = `{"refresh_token":"` + value + `"}`
				}
				put(t, filepath.Join(root, file), content)
				r, err := Discover(o)
				if err != nil {
					t.Fatal(err)
				}
				item := byName(t, r, "skill", "private")
				if item.Selectable {
					t.Fatal("credential-bearing skill was selectable")
				}
				raw, _ := json.Marshal(r)
				if strings.Contains(string(raw), value) {
					t.Fatal("opaque credential leaked into inventory or preview")
				}
				if _, err := BuildChanges(r, []string{item.ID}, nil); err == nil {
					t.Fatal("blocked credential file was importable")
				}
			})
		}
	}
}

func TestSkillCompatibilityReportsSourceSpecificBehaviorAndReferences(t *testing.T) {
	for _, tc := range []struct{ name, frontmatter, body, extraFile, extraContent string }{
		{"fork", "context: fork\nagent: Explore\n", "Text", "", ""},
		{"hooks", "hooks: {}\n", "Text", "", ""},
		{"paths", "paths: ['src/**']\n", "Text", "", ""},
		{"missing-reference", "", "Read [guide](references/missing.md).", "", ""},
		{"outside-reference", "", "Read [guide](../outside.md).", "", ""},
		{"absolute-reference", "", "Read [guide](C:/outside.md).", "", ""},
		{"source-variable", "", "Run ${CLAUDE_SKILL_DIR}/check.sh.", "", ""},
		{"script-runtime", "", "Run scripts/check.js.", "scripts/check.js", "node_repl.invoke();"},
		{"excluded-reference", "", "Read [guide](node_modules/example/README.md).", "node_modules/example/README.md", "Dependency documentation"},
		{"dependency", "", "Text", "agents/openai.yaml", "dependencies:\n  tools:\n    - type: mcp\n      value: client-only-tool\n"},
		{"bad-policy", "", "Text", "agents/openai.yaml", "policy: invalid\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := fixture(t)
			root := filepath.Join(o.Home, ".claude/skills/example")
			put(t, filepath.Join(root, "SKILL.md"), "---\nname: example\ndescription: Example\n"+tc.frontmatter+"---\n"+tc.body)
			if tc.extraFile != "" {
				put(t, filepath.Join(root, tc.extraFile), tc.extraContent)
			}
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			if byName(t, r, "skill", "example").Selectable {
				t.Fatal("unmapped behavior was considered compatible")
			}
		})
	}
}

func TestSkillCompatibilityRespectsCodexPolicyAndDisabledSettings(t *testing.T) {
	o := fixture(t)
	root := filepath.Join(o.Home, ".codex/skills/manual")
	put(t, filepath.Join(root, "SKILL.md"), "---\nname: manual\ndescription: Manual task\n---\nText")
	put(t, filepath.Join(root, "agents/openai.yaml"), "policy:\n  allow_implicit_invocation: false\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	item := byName(t, r, "skill", "manual")
	changes, err := BuildChanges(r, []string{item.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if filepath.Base(change.Path) == "SKILL.md" {
			meta, _, err := frontmatter(change.Content)
			if err != nil || meta["disable-model-invocation"] != true {
				t.Fatal("Codex manual-only policy was lost")
			}
		}
	}
	for _, target := range []string{root, filepath.Join(root, "SKILL.md")} {
		put(t, filepath.Join(o.Home, ".codex/config.toml"), "[[skills.config]]\npath = '"+filepath.ToSlash(target)+"'\nenabled = false\n")
		if _, err := BuildChanges(r, []string{item.ID}, nil); err == nil {
			t.Fatal("changed visibility settings were not detected")
		}
		r, err = Discover(o)
		if err != nil {
			t.Fatal(err)
		}
		if byName(t, r, "skill", "manual").Selectable {
			t.Fatal("disabled Codex skill was enabled by migration")
		}
	}
}

func TestSkillCompatibilityRespectsClaudeCurrentSettings(t *testing.T) {
	for _, settings := range []string{`{"skillOverrides":{"example":"off"}}`, `{"skillOverrides":{"example":"user-invocable-only"}}`, `{"permissions":{"deny":["Skill(example)"]}}`, `{"permissions":{"deny":["Skill(*)"]}}`} {
		t.Run(settings, func(t *testing.T) {
			o := fixture(t)
			o.Project = filepath.Join(o.Home, "project")
			put(t, filepath.Join(o.Home, ".claude/skills/example/SKILL.md"), "---\nname: example\ndescription: Example\n---\nText")
			p := filepath.Join(o.Project, ".claude/settings.local.json")
			put(t, p, settings)
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			if byName(t, r, "skill", "example").Selectable {
				t.Fatal("Claude visibility or denial was lost")
			}
			b, _ := os.ReadFile(p)
			if string(b) != settings {
				t.Fatal("source settings changed")
			}
		})
	}
}

func TestSkillCompatibilityChecksAncestorAndNativeRestrictions(t *testing.T) {
	for _, source := range []string{"codex", "claude"} {
		t.Run(source, func(t *testing.T) {
			o := fixture(t)
			repo := filepath.Join(o.Home, "repo")
			o.Project = filepath.Join(repo, "packages", "app")
			put(t, filepath.Join(repo, ".git"), "gitdir: fixture")
			personal := filepath.Join(o.Home, "."+source, "skills", "personal")
			put(t, filepath.Join(personal, "SKILL.md"), "---\nname: personal\ndescription: Personal task\n---\nText")
			native := filepath.Join(repo, ".claude", "skills", "folder-alias")
			if source == "codex" {
				native = filepath.Join(o.Home, ".agents", "skills", "folder-alias")
			}
			put(t, filepath.Join(native, "SKILL.md"), "---\nname: native-tool\ndescription: Native task\nallowed-tools: ['*']\n---\nText")
			var configPath, config string
			if source == "codex" {
				configPath = filepath.Join(repo, ".codex", "config.toml")
				for _, folder := range []string{personal, native} {
					config += "[[skills.config]]\npath = '" + filepath.ToSlash(folder) + "'\nenabled = false\n"
				}
			} else {
				configPath = filepath.Join(repo, ".claude", "settings.json")
				config = `{"skillOverrides":{"personal":"off","native-tool":"off"}}`
			}
			put(t, configPath, config)
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			if byName(t, r, "skill", "personal").Selectable {
				t.Fatal("ancestor restriction was ignored")
			}
			item := byName(t, r, "skill", "native-tool")
			if item.Selectable || item.Disposition != "native" || !strings.Contains(item.Detail, "independently") || !strings.Contains(item.Detail, "tool pre-approvals") {
				t.Fatalf("native restrictions missing: %+v", item)
			}
			original, _ := os.ReadFile(configPath)
			if string(original) != config {
				t.Fatal("source restriction changed")
			}
		})
	}
}

func TestSkillDocumentationIsNotMistakenForCredentialsOrMissingFiles(t *testing.T) {
	for _, tc := range []struct{ name, frontmatter, body, extraFile, extraContent string }{
		{"npm-scope", "", "Install `@playwright/cli` and `@mantine/form`.", "", ""},
		{"decorator", "", "Register tools with @mcp.tool and @app.call_tool.", "", ""},
		{"website-path", "", "See [resources](/docs/concepts/resources).", "", ""},
		{"env-reference", "", "Send `api-key: $ELEVENLABS_API_KEY` with each request.", "", ""},
		{"code-expression", "", "validate: { password: hasLength({ min: 8 }) }", "", ""},
		{"auth-placeholder", "", "Send the header `Authorization: Bearer <token>`.", "scripts/client.py", "headers = {\"Authorization\": \"Bearer \" + token}\n"},
		{"quoted-placeholder", "", "Set `password = \"your password here\"` or `api_key: \"YOUR_API_KEY_1\"`.", "", ""},
		{"metadata-version", "version: 1.2.3\nauthor: someone\n", "Text", "", ""},
		{"virtualenv", "", "Text", ".venv/lib/site-packages/certifi/cacert.pem", "-----BEGIN CERTIFICATE-----\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := fixture(t)
			root := filepath.Join(o.Home, ".claude/skills/example")
			put(t, filepath.Join(root, "SKILL.md"), "---\nname: example\ndescription: Example\n"+tc.frontmatter+"---\n"+tc.body)
			if tc.extraFile != "" {
				put(t, filepath.Join(root, tc.extraFile), tc.extraContent)
			}
			if tc.name == "virtualenv" {
				put(t, filepath.Join(root, ".venv/pyvenv.cfg"), "home = /usr/bin\n")
			}
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			item := byName(t, r, "skill", "example")
			if !item.Selectable || item.Disposition != "convertible" {
				t.Fatalf("documentation blocked the skill: %s", item.Detail)
			}
			changes, err := BuildChanges(r, []string{item.ID}, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range changes {
				if strings.Contains(filepath.ToSlash(change.Path), "/.venv/") {
					t.Fatal("a virtual environment was copied")
				}
				if filepath.Base(change.Path) == "SKILL.md" && tc.name == "metadata-version" {
					meta, _, err := frontmatter(change.Content)
					extra, _ := meta["metadata"].(map[string]any)
					if err != nil || meta["version"] != nil || extra["version"] != "1.2.3" || extra["author"] != "someone" {
						t.Fatalf("descriptive fields were not moved under metadata: %v", meta)
					}
				}
			}
		})
	}
}

func TestSkillsWithRealCredentialsStayBlocked(t *testing.T) {
	for _, content := range []string{
		"Use api_key: Zx9wLm2Qp7Rt4Kv8",
		"curl -H 'Authorization: Bearer Zx9wLm2Qp7Rt4Kv8Yb3N'",
		"password = \"Tr0ub4dor-3xample!\"",
		"export TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"password = \"Zx9wLm2(Qp7Rt4Kv8\"",
		"api_key: abc9wLm2Qp7Rt4Kv8",
		"secret: testPassw0rdZx9wLm",
		"Authorization: Bearer tokenZx9wLm2Qp7Rt4Kv8",
		"password = \"my production passphrase\"",
		"password: 'correct horse battery staple'",
		"password = \"7382916405\"",
		"password = \"say \\\"hi\\\" Zx9wLm2\"",
	} {
		o := fixture(t)
		put(t, filepath.Join(o.Home, ".claude/skills/example/SKILL.md"), "---\nname: example\ndescription: Example\n---\n"+content)
		r, err := Discover(o)
		if err != nil {
			t.Fatal(err)
		}
		if byName(t, r, "skill", "example").Selectable {
			t.Fatalf("a literal credential was importable: %q", content)
		}
	}
}

func TestIdenticalSkillInBothAppsMovesOnce(t *testing.T) {
	o := fixture(t)
	skill := "---\nname: review\ndescription: Review changes\n---\nRead [notes](notes.md).\n"
	for _, source := range []string{"claude", "codex"} {
		put(t, filepath.Join(o.Home, "."+source, "skills", "review", "SKILL.md"), skill)
		put(t, filepath.Join(o.Home, "."+source, "skills", "review", "notes.md"), "Same notes")
	}
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	var ready, covered int
	var kept Item
	for _, item := range r.Items {
		if item.Kind != "skill" || item.Name != "review" {
			continue
		}
		switch {
		case item.Selectable && item.Disposition == "convertible":
			ready++
			kept = item
		case !item.Selectable && item.Disposition == "native" && strings.Contains(item.Detail, "Identical to the codex copy"):
			covered++
		default:
			t.Fatalf("unexpected copy: %+v", item)
		}
	}
	if ready != 1 || covered != 1 || kept.Source != "codex" {
		t.Fatalf("ready=%d covered=%d kept=%s", ready, covered, kept.Source)
	}
	// The kept copy is really written, supporting file included.
	changes, err := BuildChanges(r, []string{kept.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var written []string
	for _, c := range changes {
		written = append(written, filepath.ToSlash(c.Path))
	}
	for _, want := range []string{"skills/review/SKILL.md", "skills/review/notes.md"} {
		if !slices.ContainsFunc(written, func(p string) bool { return strings.HasSuffix(p, want) }) {
			t.Fatalf("%s not written: %v", want, written)
		}
	}
}

func TestPluginInRegistryAndSettingsIsListedOnce(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude", "settings.json"), `{"enabledPlugins":{"tool@awesome-copilot":true}}`)
	put(t, filepath.Join(o.Home, ".claude", "plugins", "installed_plugins.json"), `{"version":2,"plugins":{"tool@awesome-copilot":[{}]}}`)
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, i := range r.Items {
		if i.Kind == "plugin" && i.Name == "tool@awesome-copilot" {
			rows++
			if !i.Selectable || i.Disposition != "convertible" {
				t.Fatalf("plugin not ready: %+v", i)
			}
		}
	}
	if rows != 1 {
		t.Fatalf("plugin listed %d times", rows)
	}
}

func TestSkillExamplesAndTemplateStringsAreNotBlocked(t *testing.T) {
	o := fixture(t)
	root := filepath.Join(o.Home, ".claude/skills/example")
	put(t, filepath.Join(root, "SKILL.md"), "---\nname: example\ndescription: Example\n---\nRun `inspector -e API_KEY=abc123`. See data/nuxt.csv.\n")
	put(t, filepath.Join(root, "data/nuxt.csv"), "pattern,code\nprovide,\"return { provide: { hello: (name) => `hello ${name}!` } }\"\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if item := byName(t, r, "skill", "example"); !item.Selectable || item.Disposition != "convertible" {
		t.Fatalf("example values blocked the skill: %s", item.Detail)
	}
	put(t, filepath.Join(root, "SKILL.md"), "---\nname: example\ndescription: Example\n---\nCurrent branch: !`git branch --show-current`\n")
	if r, err = Discover(o); err != nil {
		t.Fatal(err)
	}
	if byName(t, r, "skill", "example").Selectable {
		t.Fatal("an inline shell command was importable")
	}
}

func TestSameNameSkillsDifferingOnlyInModeStayAlternatives(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no executable bit")
	}
	o := fixture(t)
	for _, source := range []string{"claude", "codex"} {
		root := filepath.Join(o.Home, "."+source, "skills", "review")
		put(t, filepath.Join(root, "SKILL.md"), "---\nname: review\ndescription: Review changes\n---\nRun scripts/check.sh.\n")
		put(t, filepath.Join(root, "scripts/check.sh"), "#!/bin/sh\necho ok\n")
	}
	if err := os.Chmod(filepath.Join(o.Home, ".codex/skills/review/scripts/check.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range r.Items {
		if item.Kind == "skill" && item.Name == "review" && !item.Selectable {
			t.Fatalf("a copy with a different executable bit was treated as the same skill: %+v", item)
		}
	}
}

func TestSettingsEditsDoNotDecideWhichSkillCopyIsNewer(t *testing.T) {
	o := fixture(t)
	earlier := time.Now().Add(-2 * time.Hour)
	for _, source := range []string{"codex", "claude"} {
		root := filepath.Join(o.Home, "."+source, "skills", "review")
		put(t, filepath.Join(root, "SKILL.md"), "---\nname: review\ndescription: Review changes\n---\n"+source+" text\n")
		if source == "codex" {
			if err := os.Chtimes(filepath.Join(root, "SKILL.md"), earlier, earlier); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Codex's settings were edited most recently, but its skill is the older one.
	put(t, filepath.Join(o.Home, ".codex", "config.toml"), "model = \"x\"\n")
	if err := os.Chtimes(filepath.Join(o.Home, ".claude/skills/review/SKILL.md"), earlier.Add(time.Hour), earlier.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range r.Items {
		if item.Kind == "skill" && item.Name == "review" {
			if want := map[string]string{"claude": "convertible", "codex": "needs-review"}[item.Source]; item.Disposition != want {
				t.Fatalf("%s copy: %s (%s)", item.Source, item.Disposition, item.Detail)
			}
		}
	}
}

func TestSkillLinkIntoAVirtualEnvironmentIsBlocked(t *testing.T) {
	o := fixture(t)
	root := filepath.Join(o.Home, ".claude/skills/example")
	put(t, filepath.Join(root, "SKILL.md"), "---\nname: example\ndescription: Example\n---\nRead [notes](.venv/lib/example.md).\n")
	put(t, filepath.Join(root, ".venv/pyvenv.cfg"), "home = /usr/bin\n")
	put(t, filepath.Join(root, ".venv/lib/example.md"), "Notes")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if byName(t, r, "skill", "example").Selectable {
		t.Fatal("a link into a skipped virtual environment was importable")
	}
}

func TestRootSkillFileDecidesWhichCopyIsNewer(t *testing.T) {
	// A supporting ref/SKILL.md must not narrow recency to its own folder.
	o := fixture(t)
	old, middle := time.Now().Add(-3*time.Hour), time.Now().Add(-2*time.Hour)
	for _, source := range []string{"codex", "claude"} {
		root := filepath.Join(o.Home, "."+source, "skills", "review")
		put(t, filepath.Join(root, "SKILL.md"), "---\nname: review\ndescription: Review changes\n---\n"+source+" text\n")
		put(t, filepath.Join(root, "ref", "SKILL.md"), source+" reference\n")
		for _, file := range []string{"SKILL.md", "ref/SKILL.md"} {
			when := middle
			if source == "claude" && file == "ref/SKILL.md" {
				when = old
			} else if source == "claude" {
				when = time.Now()
			}
			if err := os.Chtimes(filepath.Join(root, file), when, when); err != nil {
				t.Fatal(err)
			}
		}
	}
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range r.Items {
		if item.Kind == "skill" && item.Name == "review" {
			if want := map[string]string{"claude": "convertible", "codex": "needs-review"}[item.Source]; item.Disposition != want {
				t.Fatalf("%s copy: %s (%s)", item.Source, item.Disposition, item.Detail)
			}
		}
	}
}

// skillCopies returns the claude and codex items for a skill name.
func skillCopies(t *testing.T, r Report, name string) map[string]Item {
	t.Helper()
	copies := map[string]Item{}
	for _, item := range r.Items {
		if item.Kind == "skill" && item.Name == name {
			copies[item.Source] = item
		}
	}
	return copies
}

func TestDifferingSkillCopiesExplainTheChoiceAndCannotBothBeSelected(t *testing.T) {
	for _, tc := range []struct{ name, codexExtra string }{{"different text", ""}, {"extra file only", "extra.md"}} {
		t.Run(tc.name, func(t *testing.T) {
			o := fixture(t)
			earlier := time.Now().Add(-time.Hour)
			for _, source := range []string{"codex", "claude"} {
				root := filepath.Join(o.Home, "."+source, "skills", "review")
				text := "same"
				if tc.codexExtra == "" {
					text = source
				}
				put(t, filepath.Join(root, "SKILL.md"), "---\nname: review\ndescription: Review changes\n---\n"+text+"\n")
				if source == "codex" {
					if tc.codexExtra != "" {
						put(t, filepath.Join(root, tc.codexExtra), "More")
						if err := os.Chtimes(filepath.Join(root, tc.codexExtra), earlier, earlier); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Chtimes(filepath.Join(root, "SKILL.md"), earlier, earlier); err != nil {
						t.Fatal(err)
					}
				}
			}
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			copies := skillCopies(t, r, "review")
			kept, other := copies["claude"], copies["codex"]
			if kept.Disposition != "convertible" || !strings.Contains(kept.Note, "The codex copy of this skill differs; this one was edited more recently, so it is selected") {
				t.Fatalf("kept copy doesn't say another was left out: %s", kept.Note)
			}
			if other.Disposition != "needs-review" || !other.Selectable || !strings.Contains(other.Note, "The claude copy of this skill differs and was edited more recently, so it is the one selected") {
				t.Fatalf("other copy: %+v", other)
			}
			_, err = BuildChanges(r, []string{kept.ID, other.ID}, nil)
			if err == nil || !strings.Contains(err.Error(), "Both the claude and the codex copy of the skill review are selected") {
				t.Fatalf("both copies selected: %v", err)
			}
		})
	}
}

func TestIdenticalSkillCopyHasNoReplaceChoiceOfItsOwn(t *testing.T) {
	o := fixture(t)
	skill := "---\nname: review\ndescription: Review changes\n---\nText\n"
	for _, source := range []string{"claude", "codex"} {
		put(t, filepath.Join(o.Home, "."+source, "skills", "review", "SKILL.md"), skill)
	}
	put(t, filepath.Join(o.Destination, "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Older\n---\nOld\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	copies := skillCopies(t, r, "review")
	if kept := copies["codex"]; !kept.Selectable || !kept.Conflict {
		t.Fatalf("kept copy should offer the replacement: %+v", kept)
	}
	if sibling := copies["claude"]; sibling.Conflict || sibling.Selectable || !strings.Contains(sibling.Detail, "Identical to the codex copy") {
		t.Fatalf("identical copy: %+v", sibling)
	}
}

func TestSkillMetadataKeepsTextAsWritten(t *testing.T) {
	o := fixture(t)
	root := filepath.Join(o.Home, ".claude/skills/example")
	put(t, filepath.Join(root, "SKILL.md"), "---\nname: example\ndescription: Example\nversion: 1.10\ntags: [a, b]\n---\nText\n")
	put(t, filepath.Join(o.Home, ".claude/skills/other/SKILL.md"), "---\nname: other\ndescription: Other\nversion: 1.2.3\nmetadata:\n  version: 9\n---\nText\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	item := byName(t, r, "skill", "example")
	changes, err := BuildChanges(r, []string{item.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var skill string
	for _, c := range changes {
		if strings.HasSuffix(filepath.ToSlash(c.Path), "example/SKILL.md") {
			skill = string(c.Content)
		}
	}
	if !strings.Contains(skill, `version: "1.10"`) || !strings.Contains(skill, "tags: a, b") {
		t.Fatalf("metadata changed: %s", skill)
	}
	if other := byName(t, r, "skill", "other"); !strings.Contains(other.Detail, "Its version (1.2.3) differs from metadata.version (9)") {
		t.Fatalf("dropped top-level version not disclosed: %s", other.Detail)
	}
}

func TestExistingSkillMetadataKeepsTextAndNotesAreRedacted(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/skills/kept/SKILL.md"), "---\nname: kept\ndescription: Kept\nversion: 1.10\nauthor: Ann\nmetadata:\n  version: 1.10\n  build: 007\n---\nText\n")
	put(t, filepath.Join(o.Home, ".claude/skills/linked/SKILL.md"), "---\nname: linked\ndescription: Linked\nhomepage: https://ann:Zx9wLm2Qp7@example.com/a\nmetadata:\n  homepage: https://example.com/b\n---\nText\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	item := byName(t, r, "skill", "kept")
	if strings.Contains(item.Detail, "differs") {
		t.Fatalf("matching metadata reported as different: %s", item.Detail)
	}
	changes, err := BuildChanges(r, []string{item.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var skill string
	for _, c := range changes {
		if strings.HasSuffix(filepath.ToSlash(c.Path), "kept/SKILL.md") {
			skill = string(c.Content)
		}
	}
	if !strings.Contains(skill, `version: "1.10"`) || !strings.Contains(skill, `build: "007"`) || !strings.Contains(skill, "author: Ann") {
		t.Fatalf("metadata changed: %s", skill)
	}
	if linked := byName(t, r, "skill", "linked"); strings.Contains(linked.Detail, "Zx9wLm2Qp7") || !strings.Contains(linked.Detail, "differs from metadata.homepage") {
		t.Fatalf("metadata note: %s", linked.Detail)
	}
}

func TestLinkedSkillFolderIsListedAndFolderViewFilesAreNotCopied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra rights on Windows")
	}
	o := fixture(t)
	elsewhere := filepath.Join(o.Home, "elsewhere", "linked")
	put(t, filepath.Join(elsewhere, "SKILL.md"), "---\nname: linked\ndescription: Linked\n---\nText\n")
	if err := os.MkdirAll(filepath.Join(o.Home, ".claude/skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(o.Home, ".claude/skills/linked")); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(o.Home, ".claude/skills/plain/SKILL.md"), "---\nname: plain\ndescription: Plain\n---\nText\n")
	put(t, filepath.Join(o.Home, ".claude/skills/plain/.DS_Store"), "\x00\x00")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if linked := byName(t, r, "skill", "linked"); linked.Selectable || linked.Disposition != "needs-review" || !strings.Contains(linked.Detail, "is a link to") {
		t.Fatalf("linked skill: %+v", linked)
	}
	for _, f := range byName(t, r, "skill", "plain").Files {
		if f.Path == ".DS_Store" {
			t.Fatal(".DS_Store would be copied")
		}
	}
}

func TestSkillLinkAndInlineCommandEdges(t *testing.T) {
	for _, tc := range []struct {
		name, body, extra string
		importable        bool
	}{
		{"virtualenv folder", "Use [env](.venv).", ".venv/pyvenv.cfg", false},
		{"root folder link", "See [Home](/) and [tmp](/tmp).", "", true},
		{"command in brackets", "Branch (!`git branch --show-current`).", "", false},
		{"command fence", "```!\ngit status\n```", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := fixture(t)
			root := filepath.Join(o.Home, ".claude/skills/example")
			put(t, filepath.Join(root, "SKILL.md"), "---\nname: example\ndescription: Example\n---\n"+tc.body+"\n")
			if tc.extra != "" {
				put(t, filepath.Join(root, tc.extra), "home = /usr/bin\n")
			}
			r, err := Discover(o)
			if err != nil {
				t.Fatal(err)
			}
			if item := byName(t, r, "skill", "example"); item.Selectable != tc.importable {
				t.Fatalf("importable=%v, want %v: %s", item.Selectable, tc.importable, item.Detail)
			}
		})
	}
}

func TestInstructionsWithPlaceholdersImport(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/CLAUDE.md"), "Send `Authorization: Bearer <token>` and `api_key: $ELEVENLABS_API_KEY`.\nUse `password: your_password_here` in examples.\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if item := byName(t, r, "instructions", "CLAUDE.md"); !item.Selectable || strings.Contains(item.Preview, "omitted") {
		t.Fatalf("placeholders blocked the instructions: %+v", item)
	}
	put(t, filepath.Join(o.Home, ".claude/CLAUDE.md"), "Deploy with password = \"Zx9wLm2Qp7Rt4Kv8\".\n")
	if r, err = Discover(o); err != nil {
		t.Fatal(err)
	}
	if item := byName(t, r, "instructions", "CLAUDE.md"); item.Selectable || strings.Contains(item.Preview, "Zx9wLm2Qp7Rt4Kv8") {
		t.Fatalf("a literal credential in instructions: %+v", item)
	}
}

func TestLowercaseSkillFileIsFound(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/skills/lower/skill.md"), "---\nname: lower\ndescription: Lower\n---\nText\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	item := byName(t, r, "skill", "lower")
	changes, err := BuildChanges(r, []string{item.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range changes {
		names = append(names, filepath.Base(c.Path))
	}
	if !slices.Contains(names, "SKILL.md") || slices.Contains(names, "skill.md") {
		t.Fatalf("written: %v", names)
	}
}

func TestMetadataOnlySkillKeepsText(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/skills/plain/SKILL.md"), "---\nname: plain\ndescription: Plain\nmetadata:\n  version: 1.10\n  build: 007\n---\nText\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	item := byName(t, r, "skill", "plain")
	changes, err := BuildChanges(r, []string{item.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var skill string
	for _, c := range changes {
		if strings.HasSuffix(filepath.ToSlash(c.Path), "plain/SKILL.md") {
			skill = string(c.Content)
		}
	}
	if !strings.Contains(skill, `version: "1.10"`) || !strings.Contains(skill, `build: "007"`) {
		t.Fatalf("metadata changed: %s", skill)
	}
}

func TestQuotedCredentialInSkillMetadataBlocks(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/skills/quoted/SKILL.md"), "---\nname: quoted\ndescription: Quoted\nmetadata:\n  secret: \"correct-horse-staple\"\n---\nText\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	quoted := byName(t, r, "skill", "quoted")
	if quoted.Selectable || strings.Contains(quoted.Preview, "correct-horse-staple") {
		t.Fatalf("quoted credential in metadata: %+v", quoted)
	}
	for _, f := range quoted.Files {
		if strings.Contains(f.Preview, "correct-horse-staple") {
			t.Fatalf("file preview shows the credential: %+v", f)
		}
	}
}

func TestLinkToLowercaseSkillFileNeedsChanging(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/skills/lower/skill.md"), "---\nname: lower\ndescription: Lower\n---\nSee [notes](notes.md).\n")
	put(t, filepath.Join(o.Home, ".claude/skills/lower/notes.md"), "Back to [the skill](skill.md).\n")
	put(t, filepath.Join(o.Home, ".claude/skills/upper/SKILL.md"), "---\nname: upper\ndescription: Upper\n---\nSee [notes](notes.md).\n")
	put(t, filepath.Join(o.Home, ".claude/skills/upper/notes.md"), "Back to [the skill](SKILL.md#top).\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	if lower := byName(t, r, "skill", "lower"); lower.Selectable || !strings.Contains(lower.Detail, "must say SKILL.md") {
		t.Fatalf("link to skill.md: %+v", lower)
	}
	if upper := byName(t, r, "skill", "upper"); !upper.Selectable {
		t.Fatalf("link to SKILL.md: %s", upper.Detail)
	}
}

func TestSkillCopiesWithTheSameTimeOrAnExistingSkillSayWhy(t *testing.T) {
	for _, existing := range []bool{false, true} {
		o := fixture(t)
		when := time.Now().Add(-time.Hour)
		for _, source := range []string{"codex", "claude"} {
			path := filepath.Join(o.Home, "."+source, "skills", "review", "SKILL.md")
			put(t, path, "---\nname: review\ndescription: Review changes\n---\n"+source+"\n")
			if err := os.Chtimes(path, when, when); err != nil {
				t.Fatal(err)
			}
		}
		if existing {
			put(t, filepath.Join(o.Destination, "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Older\n---\nOld\n")
		}
		r, err := Discover(o)
		if err != nil {
			t.Fatal(err)
		}
		copies := skillCopies(t, r, "review")
		var kept, other Item
		for _, c := range copies {
			if strings.Contains(c.Note, "; this one") {
				kept = c
			} else {
				other = c
			}
		}
		if !strings.Contains(kept.Note, "last changed at the same time") || strings.Contains(kept.Note+other.Note, "edited more recently") {
			t.Fatalf("tie: %q / %q", kept.Note, other.Note)
		}
		if selected := strings.Contains(kept.Note+other.Note, "selected"); selected == existing {
			t.Fatalf("existing skill %v: %q / %q", existing, kept.Note, other.Note)
		}
	}
}

func TestSkillLinkedIntoTheSharedFolderIsAlreadyAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra rights on Windows")
	}
	o := fixture(t)
	shared := filepath.Join(o.Home, ".agents", "skills", "linked")
	put(t, filepath.Join(shared, "SKILL.md"), "---\nname: linked\ndescription: Linked\n---\nText\n")
	if err := os.MkdirAll(filepath.Join(o.Home, ".claude/skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(o.Home, ".claude/skills/linked")); err != nil {
		t.Fatal(err)
	}
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range r.Items {
		if i.Kind == "skill" && i.Name == "linked" && i.Disposition != "native" {
			t.Fatalf("linked copy is work to do: %+v", i)
		}
	}
}

func TestSkillFrontmatterKeepsOrderCommentsNullsAndLists(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/skills/review/SKILL.md"), "\ufeff---\n# The review skill\nname: review\ndescription: Review changes # short\nlicense: MIT\nversion: 1.10\ntags: [a, \"b, c\"]\nhomepage: null\n---\nText\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	item := byName(t, r, "skill", "review")
	changes, err := BuildChanges(r, []string{item.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var skill string
	for _, c := range changes {
		if strings.HasSuffix(filepath.ToSlash(c.Path), "review/SKILL.md") {
			skill = string(c.Content)
		}
	}
	for _, want := range []string{"# The review skill", "description: Review changes # short", `version: "1.10"`, "homepage: null", "- a", "- b, c"} {
		if !strings.Contains(skill, want) {
			t.Fatalf("missing %q in:\n%s", want, skill)
		}
	}
	if !(strings.Index(skill, "name:") < strings.Index(skill, "description:") && strings.Index(skill, "description:") < strings.Index(skill, "license:") && strings.Index(skill, "license:") < strings.Index(skill, "metadata:")) {
		t.Fatalf("key order changed:\n%s", skill)
	}
}

func TestSkillFileKeepsTheNameAsWritten(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "skill.md"), "x")
	if got := filepath.Base(skillFile(dir)); got != "skill.md" {
		t.Fatalf("skill file: %s", got)
	}
	put(t, filepath.Join(dir, "other", "SKILL.md"), "x")
	if got := filepath.Base(skillFile(filepath.Join(dir, "other"))); got != "SKILL.md" {
		t.Fatalf("skill file: %s", got)
	}
}

func TestSkillFrontmatterWithAnchorsIsWrittenInFull(t *testing.T) {
	o := fixture(t)
	put(t, filepath.Join(o.Home, ".claude/skills/shared/SKILL.md"), "---\nname: shared\nauthor: &who Ann\ndescription: *who\n---\nText\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	item := byName(t, r, "skill", "shared")
	changes, err := BuildChanges(r, []string{item.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if strings.HasSuffix(filepath.ToSlash(c.Path), "shared/SKILL.md") {
			meta, _, err := frontmatter(c.Content)
			if err != nil || meta["description"] != "Ann" || strings.Contains(string(c.Content), "*who") {
				t.Fatalf("frontmatter: %v\n%s", err, c.Content)
			}
			return
		}
	}
	t.Fatal("skill not written")
}

func TestSkillCopiesWithASharedSkillAreNotSaidToBeSelected(t *testing.T) {
	o := fixture(t)
	earlier := time.Now().Add(-time.Hour)
	for _, source := range []string{"codex", "claude"} {
		path := filepath.Join(o.Home, "."+source, "skills", "review", "SKILL.md")
		put(t, path, "---\nname: review\ndescription: Review changes\n---\n"+source+"\n")
		if source == "codex" {
			if err := os.Chtimes(path, earlier, earlier); err != nil {
				t.Fatal(err)
			}
		}
	}
	put(t, filepath.Join(o.Home, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Shared\n---\nShared\n")
	r, err := Discover(o)
	if err != nil {
		t.Fatal(err)
	}
	for source, c := range skillCopies(t, r, "review") {
		if source == "shared" {
			continue
		}
		if c.Disposition != "needs-review" || strings.Contains(c.Note, "selected.") && !strings.Contains(c.Note, "no copy is selected") || !strings.Contains(c.Note, "shared skill") {
			t.Fatalf("%s copy: %s / %q", source, c.Disposition, c.Note)
		}
	}
}
