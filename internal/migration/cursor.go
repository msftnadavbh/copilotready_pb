package migration

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Cursor keeps its personal agent setup in ~/.cursor (mcp.json, commands, skills, agents,
// hooks.json) and its project setup in each repository's .cursor folder. Copilot reads none of
// these files, so each one is converted or shown for review.
// https://cursor.com/docs/rules, /docs/context/mcp, /docs/skills, /docs/subagents, /docs/hooks

// cursor scans what is specific to Cursor's home folder. Skills, agents and commands go through
// the scanners shared with the other apps.
func (s *scanner) cursor(root string) {
	s.mcpFile("cursor", filepath.Join(root, "mcp.json"), false)
	s.cursorHooks("cursor", filepath.Join(root, "hooks.json"))
	s.info("cursor", "instructions", "User Rules", "unsupported", "Cursor keeps User Rules in its settings, not in a file, so they can't be read. If you set any in Cursor Settings → Rules, copy them into Copilot's personal instructions (copilot-instructions.md in your Copilot folder).")
	if exists(filepath.Join(root, "cli-config.json")) {
		s.info("cursor", "permissions", "Cursor CLI permissions", "unsupported", "Saved allow and deny lists stay in Cursor. Approvals are never copied; Copilot asks for its own.")
	}
	for _, folder := range []string{"plugins/local", "plugins/marketplaces", "plugins/cache"} {
		if entries, e := os.ReadDir(filepath.Join(root, folder)); e == nil && len(entries) > 0 {
			s.info("cursor", "plugin", "Cursor plugins", "unsupported", "Copilot can't install Cursor plugins. Skills and MCP servers you added to Cursor yourself are listed separately.")
			break
		}
	}
}

// cursorProject lists a repository's Cursor files. Copilot Ready doesn't write into
// repositories, so rules, servers, skills and agents are shown with what they would become.
func (s *scanner) cursorProject(root string) {
	s.cursorRules(root, "")
	if p := filepath.Join(root, ".cursorrules"); exists(p) {
		c := candidate{}
		if b, _, e := s.source(p, &c); e == nil {
			s.cursorRule(".cursorrules", p, b, "cursorrules", "", c)
		}
	}
	s.mcpFile("cursor-project", filepath.Join(root, ".cursor", "mcp.json"), false)
	for _, kind := range []struct{ dir, kind, target string }{{"skills", "skill", ".github/skills"}, {"agents", "agent", ".github/agents"}} {
		if entries, e := os.ReadDir(filepath.Join(root, ".cursor", kind.dir)); e == nil && len(entries) > 0 {
			s.info("cursor-project", kind.kind, ".cursor/"+kind.dir, "needs-review", "Copilot reads project "+kind.dir+" from "+kind.target+", not .cursor/"+kind.dir+". Copy them there to use them in Copilot; no repository files will be changed.")
		}
	}
	s.prompts("cursor-project", filepath.Join(root, ".cursor", "commands"), false)
	s.cursorHooks("cursor-project", filepath.Join(root, ".cursor", "hooks.json"))
	if exists(filepath.Join(root, ".cursorignore")) {
		s.info("cursor-project", "settings", ".cursorignore", "unsupported", "Copilot has no file that hides content from the agent, so these paths are not excluded in Copilot.")
	}
	if exists(filepath.Join(root, ".cursor", "BUGBOT.md")) {
		s.info("cursor-project", "instructions", "BUGBOT.md", "unsupported", "These are instructions for Cursor's review bot. Copilot code review reads .github/copilot-instructions.md instead.")
	}
	if exists(filepath.Join(root, ".cursor", "cli.json")) {
		s.info("cursor-project", "permissions", ".cursor/cli.json", "unsupported", "Saved allow and deny lists stay in Cursor. Approvals are never copied; Copilot asks for its own.")
	}
}

// cursorRules lists the rules in root/.cursor/rules. scope is root's folder relative to the
// selected project for rules Cursor reads from a subfolder, and "" otherwise.
func (s *scanner) cursorRules(root, scope string) {
	dir := filepath.Join(root, ".cursor", "rules")
	s.walkFiles(dir, func(p string) {
		// Cursor reads only .mdc files here; a plain .md file in the folder is ignored.
		if !strings.EqualFold(filepath.Ext(p), ".mdc") {
			return
		}
		c := candidate{}
		b, _, e := s.source(p, &c)
		if e != nil {
			s.warn(e.Error())
			return
		}
		rel, _ := filepath.Rel(dir, p)
		name := filepath.ToSlash(rel)
		if scope != "" {
			name = scope + "/.cursor/rules/" + name
		}
		s.cursorRule(name, p, b, strings.TrimSuffix(path.Join(scope, filepath.ToSlash(rel)), filepath.Ext(rel)), scope, c)
	})
}

// cursorNestedRules finds .cursor/rules folders below the selected project, which Cursor applies
// to the files in their folder. Dependency, build and hidden folders are skipped, and the search
// stops after 6 levels or 5,000 folders.
func (s *scanner) cursorNestedRules(project string) {
	visited := 0
	_ = filepath.WalkDir(project, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(project, p)
		if rel == "." {
			return nil
		}
		if visited++; visited > 5000 {
			return filepath.SkipAll
		}
		name := d.Name()
		if strings.Count(filepath.ToSlash(rel), "/") >= 6 || strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "dist" || name == "build" || name == "target" || name == "__pycache__" || exists(filepath.Join(p, "pyvenv.cfg")) {
			return filepath.SkipDir
		}
		if exists(filepath.Join(p, ".cursor", "rules")) {
			s.cursorRules(p, filepath.ToSlash(rel))
		}
		return nil
	})
}

// cursorRule shows a rule as the Copilot instructions file it would become. Cursor has four
// kinds of rule; Copilot instructions apply either always or to matching files, so rules that
// Cursor's agent picks by description, or that are only @-mentioned, have no equivalent. A rule
// from a subfolder (scope) applies only to files there, so applyTo starts with that folder.
func (s *scanner) cursorRule(name, file string, b []byte, target, scope string, c candidate) {
	header, body, ok := cursorRuleHeader(b)
	i := Item{Source: "cursor-project", Kind: "rules", Name: name, Disposition: "needs-review"}
	where := " From " + file + "."
	if !ok {
		i.Detail = "The rule's header can't be read. Review it in Cursor." + where
		s.add(i, c)
		return
	}
	dest := ".github/instructions/" + safeName(target) + ".instructions.md"
	globs := header.globs
	if header.always || filepath.Base(file) == ".cursorrules" {
		globs = []string{"**"}
	}
	if scope != "" {
		for n, g := range globs {
			globs[n] = path.Join(scope, strings.TrimPrefix(strings.TrimPrefix(g, "./"), "/"))
		}
	}
	apply := strings.Join(globs, ",")
	switch {
	case header.always || filepath.Base(file) == ".cursorrules":
		i.Detail = "Cursor applies this rule to every request. "
	case apply != "":
		i.Detail = "Cursor applies this rule to matching files. "
	case header.description != "":
		i.Detail = "Cursor's agent reads this rule when it decides the description fits. Copilot instructions can't be loaded that way; a skill with the same description is the closest match. "
		apply = ""
	default:
		i.Detail = "Cursor uses this rule only when you @-mention it. Copilot instructions have no equivalent; a skill you invoke yourself is the closest match. "
	}
	if apply == "" {
		i.Preview = redactedPreview(b)
	} else {
		meta, _ := yaml.Marshal(map[string]any{"applyTo": apply})
		i.Preview = redactedPreview(append(append([]byte("---\n"), meta...), []byte("---\n"+string(body))...))
		i.Detail += "Copilot reads project rules from .github/instructions, so the preview shows " + dest + ". Copy it there to use it in Copilot; no repository files will be changed."
	}
	if scope != "" && apply != "" {
		i.Detail += " Cursor uses it only for files in " + scope + ", so applyTo starts with that folder."
	}
	if literalSecret(b) {
		i.Detail += " Credential-like text needs removing first" + secretWhere(b) + "."
	}
	i.Detail += where
	s.add(i, c)
}

type cursorHeader struct {
	description string
	globs       []string
	always      bool
}

var cursorHeaderLine = regexp.MustCompile(`^(description|globs|alwaysApply)\s*:\s*(.*?)\s*$`)

// cursorRuleHeader reads a rule's description, globs and alwaysApply. Cursor doesn't parse the
// header as YAML, and rules commonly say `globs: *.ts`, which YAML reads as an alias, so a header
// YAML rejects is read line by line instead.
func cursorRuleHeader(b []byte) (cursorHeader, []byte, bool) {
	var h cursorHeader
	meta, body, e := frontmatter(b)
	if e == nil {
		h.description = strings.TrimSpace(str(meta["description"]))
		switch always := meta["alwaysApply"].(type) {
		case bool:
			h.always = always
		case string:
			h.always = strings.EqualFold(always, "true")
		}
		switch globs := meta["globs"].(type) {
		case string:
			h.globs = splitGlobs(globs)
		case []any:
			for _, g := range globs {
				h.globs = append(h.globs, splitGlobs(str(g))...)
			}
		}
		return h, body, true
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(b), "\ufeff"), "\r\n", "\n")
	end := strings.Index(text[min(4, len(text)):], "\n---")
	if !strings.HasPrefix(text, "---\n") || end < 0 {
		return h, nil, false
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		m := cursorHeaderLine.FindStringSubmatch(line)
		if m == nil {
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				return h, nil, false
			}
			continue
		}
		value := unquote(m[2])
		switch m[1] {
		case "description":
			h.description = value
		case "globs":
			h.globs = splitGlobs(value)
		case "alwaysApply":
			h.always = strings.EqualFold(value, "true")
		}
	}
	return h, []byte(strings.TrimPrefix(text[4+end+4:], "\n")), true
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// splitGlobs splits a comma-separated globs value. Commas inside {} or [] belong to one glob,
// and brace alternatives are written out, so **/*.{ts,tsx} becomes **/*.ts and **/*.tsx: Copilot's
// applyTo also separates globs with commas, and a folder prefix then applies to each.
func splitGlobs(v string) []string {
	var out []string
	for _, g := range splitOutside(v) {
		if g = unquote(strings.TrimSpace(g)); g != "" {
			out = append(out, expandBraces(g)...)
		}
	}
	return out
}

// splitOutside splits v at the commas that are not inside {} or [].
func splitOutside(v string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range v {
		switch r {
		case '{', '[':
			depth++
		case '}', ']':
			depth = max(depth-1, 0)
		case ',':
			if depth == 0 {
				parts = append(parts, v[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, v[start:])
}

// expandBraces writes out a glob's {a,b} alternatives. Braces without a comma, as in
// file{1..3}.ts, are kept as written. A glob that would expand to more than 64 globs is kept
// whole; the limit is checked as it expands, so many brace groups can't make the work explode.
func expandBraces(g string) []string {
	work, out := []string{g}, []string(nil)
	for len(work) > 0 {
		w := work[len(work)-1]
		work = work[:len(work)-1]
		open, end, alts := braceAlternatives(w)
		if alts == nil {
			out = append(out, w)
			continue
		}
		for i := len(alts) - 1; i >= 0; i-- {
			work = append(work, w[:open]+alts[i]+w[end+1:])
		}
		if len(work)+len(out) > 64 {
			return []string{g}
		}
	}
	return out
}

// braceAlternatives finds the first {} group in g with more than one comma-separated
// alternative, and returns where it opens and closes and its alternatives.
func braceAlternatives(g string) (int, int, []string) {
	for open := strings.IndexByte(g, '{'); open >= 0; {
		depth := 0
		for end := open; end < len(g); end++ {
			switch g[end] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					if alts := splitOutside(g[open+1 : end]); len(alts) > 1 {
						return open, end, alts
					}
					end = len(g)
				}
			}
		}
		next := strings.IndexByte(g[open+1:], '{')
		if next < 0 {
			break
		}
		open += next + 1
	}
	return 0, 0, nil
}

// cursorHookEvents are Cursor's hook events that Copilot has under the same or another name.
var cursorHookEvents = map[string]string{"sessionStart": "sessionStart", "sessionEnd": "sessionEnd", "preToolUse": "preToolUse", "postToolUse": "postToolUse", "postToolUseFailure": "postToolUseFailure", "subagentStart": "subagentStart", "subagentStop": "subagentStop", "preCompact": "preCompact", "beforeSubmitPrompt": "userPromptSubmitted", "stop": "agentStop"}

// cursorHooks lists Cursor hooks for review only. Cursor sends and expects different JSON than
// Copilot, and lets an action through when a hook fails unless failClosed is set, where Copilot's
// preToolUse blocks it. Installing them unchanged would change what they do.
func (s *scanner) cursorHooks(source, p string) {
	m, e := readMap(p)
	if e != nil {
		if exists(p) {
			s.warn("Cannot parse Cursor hooks in " + p)
		}
		return
	}
	events := mapValue(m["hooks"])
	for _, event := range sortedKeys(events) {
		detail := "Cursor hooks send and expect different data than Copilot's, and Cursor lets an action through when a hook fails. They aren't installed; recreate this hook in Copilot if you need it."
		if target := cursorHookEvents[event]; target != "" {
			detail += " Copilot's matching event is " + target + "."
		} else {
			detail += " Copilot has no " + event + " event."
		}
		s.info(source, "hooks", event, "needs-review", detail)
	}
}

var cursorVariable = regexp.MustCompile(`\$\{(env:[A-Za-z_][A-Za-z0-9_]*|userHome|pathSeparator|/|workspaceFolder|workspaceFolderBasename)\}`)

// cursorMCP rewrites Cursor's variables into the ${NAME} references Copilot's MCP configuration
// uses, and fills in the ones that don't depend on a project. Values are never resolved.
func cursorMCP(cfg map[string]any, home string) (map[string]any, []string) {
	var reasons []string
	project := false
	var rewrite func(any) any
	rewrite = func(v any) any {
		switch x := v.(type) {
		case string:
			return cursorVariable.ReplaceAllStringFunc(x, func(m string) string {
				name := m[2 : len(m)-1]
				switch {
				case strings.HasPrefix(name, "env:"):
					return "${" + strings.TrimPrefix(name, "env:") + "}"
				case name == "userHome":
					return home
				case name == "pathSeparator" || name == "/":
					return string(filepath.Separator)
				}
				project = true
				return m
			})
		case []any:
			out := make([]any, len(x))
			for n, item := range x {
				out[n] = rewrite(item)
			}
			return out
		case map[string]any:
			out := map[string]any{}
			for k, item := range x {
				out[k] = rewrite(item)
			}
			return out
		}
		return v
	}
	out := map[string]any{}
	for k, v := range cfg {
		switch k {
		case "envFile":
			reasons = append(reasons, "Cursor loads this server's variables from "+str(v)+". Copilot doesn't read env files; set the variables in the environment Copilot starts from.")
		case "auth":
			reasons = append(reasons, "Cursor signs in to this server with its own OAuth client settings, which aren't carried over. Copilot asks you to sign in when it connects, if the server allows that.")
		default:
			out[k] = rewrite(v)
		}
	}
	if project {
		reasons = append(reasons, "It uses the project folder (${workspaceFolder}), which Copilot doesn't fill in. Replace it with the folder's path.")
	}
	return out, reasons
}
