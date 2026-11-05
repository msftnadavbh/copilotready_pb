// Package migration inventories only named customization locations. Discovery
// never executes a source tool, hook, skill, plugin or MCP process, except that it
// asks Codex's app server for its saved conversation list, a read-only call.
package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"copilotready/internal/core"
	"copilotready/internal/sessions"
	"github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"
)

type Options struct {
	Home, Project, Destination, Mode, CodexHome, ClaudeHome, CopilotHome, CursorHome string
	// SessionRecord lists conversations already imported; conversations are offered only when set.
	SessionRecord, CodexExecutable string
	// Cursor turns on Cursor as a source. It is off until Cursor support is tested with real
	// Cursor installs.
	Cursor bool
}
type SourceState struct {
	Name       string `json:"name"`
	Root       string `json:"root"`
	Present    bool   `json:"present"`
	Executable string `json:"executable,omitempty"`
	Version    string `json:"version"`
}
type Item struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Disposition string `json:"disposition"`
	Detail      string `json:"detail"`
	// Note is shown on the item's row itself: why one copy of a same-name skill is chosen.
	Note           string        `json:"note,omitempty"`
	Preview        string        `json:"preview"`
	Destination    string        `json:"destination,omitempty"`
	Selectable     bool          `json:"selectable"`
	Conflict       bool          `json:"conflict"`
	ActivationKind string        `json:"activationKind,omitempty"`
	Files          []FilePreview `json:"files,omitempty"`
}
type FilePreview struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable"`
	Text       bool   `json:"text"`
	Preview    string `json:"preview,omitempty"`
	Truncated  bool   `json:"truncated"`
}
type Report struct {
	Sources       []SourceState `json:"sources"`
	Items         []Item        `json:"items"`
	Warnings      []string      `json:"warnings"`
	Options       Options       `json:"-"`
	candidates    map[string]candidate
	snapshots     map[string]snapshot
	conversations map[string][]sessions.Session
}
type Activation struct {
	Kind, Name, Specification                            string
	Config                                               map[string]any
	HookSource, HookCommand, HookEvent, WorkingDirectory string
	TimeoutSeconds                                       int
	// MarketplaceSource is the `copilot plugin marketplace add` argument recorded by the source client.
	MarketplaceSource string `json:",omitempty"`
}
type snapshot struct {
	hash    string
	content []byte
	mode    uint32
}
type operation struct {
	path       string
	content    []byte
	appendText string
	patch      map[string]any
	// pointers sets values at JSON pointers such as /mcpServers/name. Without replace, an
	// existing value at a pointer is a conflict that needs approval.
	pointers map[string]any
	replace  bool
	mode     uint32
}
type candidate struct {
	sources    map[string]string
	ops        []operation
	activation *Activation
}
type scanner struct{ report Report }

const maxFileSize = 1 << 20

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)
var secretText = regexp.MustCompile(`(?i)(?:\b(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]{12,})\b|(?:api[_-]?key|password|secret|access[_-]?token|authorization)\s*[:=]\s*["']?[^\s"']{6,}|--(?:token|api[_-]?key|password|secret|authorization)(?:=|\s+)\S+)`)
var importReference = regexp.MustCompile(`(?:^|\s)@([A-Za-z0-9_./-]+)`)
var externalReference = regexp.MustCompile(`(?:^|\s)@(?:~[/\\]|/|[A-Za-z]:[/\\])`)

func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func exists(p string) bool   { _, e := os.Stat(p); return e == nil }
func choosePath(explicit, env, def string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}
func under(root, p string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}
func noLinks(p string) error {
	p = filepath.Clean(p)
	for {
		fi, e := os.Lstat(p)
		if e == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("linked paths need manual review: %s", p)
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		up := filepath.Dir(p)
		if up == p {
			break
		}
		p = up
	}
	return nil
}
func readFile(p string) ([]byte, uint32, error) {
	if e := noLinks(p); e != nil {
		return nil, 0, e
	}
	fi, e := os.Stat(p)
	if e != nil {
		return nil, 0, e
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxFileSize {
		return nil, 0, fmt.Errorf("not a regular customization file under 1 MiB: %s", p)
	}
	b, e := os.ReadFile(p)
	return b, uint32(fi.Mode().Perm()), e
}
func readMap(p string) (map[string]any, error) {
	b, _, e := readFile(p)
	if e != nil {
		return nil, e
	}
	return parseJSON(b)
}
func parseJSON(b []byte) (map[string]any, error) {
	v, e := hujson.Parse(b)
	if e != nil {
		return nil, e
	}
	v.Standardize()
	var m map[string]any
	e = json.Unmarshal(v.Pack(), &m)
	if e == nil && m == nil {
		e = errors.New("expected JSON object")
	}
	return m, e
}
func encode(v any) []byte { b, _ := json.MarshalIndent(v, "", "  "); return append(b, '\n') }
func safeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	v := strings.Trim(b.String(), "-")
	if v == "" {
		return "import"
	}
	if len(v) > 70 {
		v = v[:70]
	}
	return v
}
func jsonPointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
func mapValue(v any) map[string]any { m, _ := v.(map[string]any); return m }
func str(v any) string              { s, _ := v.(string); return s }
func sortedKeys(m map[string]any) []string {
	a := make([]string, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Strings(a)
	return a
}
func redactedPreview(b []byte) string {
	v := redactSecrets(string(b))
	if len(v) > 5000 {
		v = v[:5000] + "\n… preview truncated"
	}
	return v
}

func Discover(o Options) (Report, error) {
	if o.Home == "" {
		var e error
		o.Home, e = os.UserHomeDir()
		if e != nil {
			return Report{}, e
		}
	}
	var e error
	o.Home, e = filepath.Abs(o.Home)
	if e != nil {
		return Report{}, e
	}
	if o.Destination == "" {
		return Report{}, errors.New("destination profile is required")
	}
	o.Destination, e = filepath.Abs(o.Destination)
	if e != nil {
		return Report{}, e
	}
	if e = noLinks(o.Destination); e != nil {
		return Report{}, e
	}
	if o.Mode != "existing" && o.Mode != "new" {
		return Report{}, errors.New("mode must be existing or new")
	}
	o.CodexHome = choosePath(o.CodexHome, "CODEX_HOME", filepath.Join(o.Home, ".codex"))
	customClaudeHome := o.ClaudeHome != "" || os.Getenv("CLAUDE_CONFIG_DIR") != ""
	o.ClaudeHome = choosePath(o.ClaudeHome, "CLAUDE_CONFIG_DIR", filepath.Join(o.Home, ".claude"))
	claudeConfig := filepath.Join(o.Home, ".claude.json")
	if customClaudeHome {
		claudeConfig = filepath.Join(o.ClaudeHome, ".claude.json")
	}
	if o.Project != "" {
		o.Project, e = filepath.Abs(o.Project)
		if e != nil {
			return Report{}, e
		}
	}
	o.CopilotHome = choosePath(o.CopilotHome, "COPILOT_HOME", filepath.Join(o.Home, ".copilot"))
	if o.CursorHome == "" {
		o.CursorHome = filepath.Join(o.Home, ".cursor")
	}
	s := scanner{Report{Options: o, candidates: map[string]candidate{}, snapshots: map[string]snapshot{}, conversations: map[string][]sessions.Session{}, Items: []Item{}, Sources: []SourceState{}, Warnings: []string{}}}
	sources := []struct{ name, root, exe string }{{"copilot", o.CopilotHome, "copilot"}, {"codex", o.CodexHome, "codex"}, {"claude", o.ClaudeHome, "claude"}}
	if o.Cursor {
		sources = append(sources, struct{ name, root, exe string }{"cursor", o.CursorHome, "cursor-agent"})
	}
	for _, src := range sources {
		root, err := filepath.Abs(src.root)
		if err != nil {
			return Report{}, err
		}
		executable, _ := exec.LookPath(src.exe)
		state := SourceState{Name: src.name, Root: root, Present: exists(root), Executable: executable, Version: passiveVersion(src.name, executable)}
		if src.name == "claude" && exists(claudeConfig) {
			state.Present = true
		}
		if b, _, err := readFile(filepath.Join(root, "version.json")); err == nil {
			var v map[string]any
			if json.Unmarshal(b, &v) == nil {
				if val := str(v["version"]); val != "" {
					state.Version = val + " (metadata; not runtime-verified)"
				}
			}
		}
		s.report.Sources = append(s.report.Sources, state)
		if !state.Present {
			continue
		}
		if err = noLinks(root); err != nil {
			s.warn(err.Error())
			continue
		}
		native := src.name == "copilot" && samePath(root, o.Destination)
		switch src.name {
		case "copilot":
			s.instructions(src.name, root, "copilot-instructions.md", native)
			s.rules(src.name, root, "instructions", native)
			s.settings(src.name, filepath.Join(root, "settings.json"), native)
			s.mcpFile(src.name, filepath.Join(root, "mcp-config.json"), native)
			s.hookFiles(src.name, root, native)
			if exists(filepath.Join(root, "permissions-config.json")) {
				s.info(src.name, "permissions", "Saved tool and folder permissions", "needs-review", "Saved approvals stay in the existing profile. They are not copied into a new profile or treated as new folder-trust grants.")
			}
		case "codex":
			s.instructions(src.name, root, "AGENTS.md", false)
			s.codexConfig(root)
			s.hookFiles(src.name, root, false)
			s.automations(root)
			s.walkFiles(filepath.Join(root, "memories"), func(p string) {
				if strings.HasSuffix(p, ".md") {
					s.memory(src.name, p)
				}
			})
		case "claude":
			s.instructions(src.name, root, "CLAUDE.md", false)
			s.rules(src.name, root, "rules", false)
			s.settings(src.name, filepath.Join(root, "settings.json"), false)
			s.hookFiles(src.name, root, false)
			s.mcpFile(src.name, claudeConfig, false)
			s.mcpFile(src.name, filepath.Join(root, ".mcp.json"), false)
			s.claudeMemory(root)
		case "cursor":
			s.cursor(root)
		}
		if exists(filepath.Join(root, "MEMORY.md")) {
			s.memory(src.name, filepath.Join(root, "MEMORY.md"))
		}
		s.skills(src.name, filepath.Join(root, "skills"), native)
		s.agents(src.name, filepath.Join(root, "agents"), native)
		s.prompts(src.name, filepath.Join(root, "prompts"), native)
		s.prompts(src.name, filepath.Join(root, "commands"), native)
		if src.name != "cursor" {
			s.plugins(src.name, root, native)
		}
	}
	s.skills("shared", filepath.Join(o.Home, ".agents", "skills"), true)
	if o.SessionRecord != "" {
		s.conversations()
	}
	if o.Project != "" {
		if p, err := filepath.Abs(o.Project); err == nil {
			s.project(p)
		} else {
			return Report{}, err
		}
	}
	s.reviewCompetingImports()
	return s.report, nil
}

// settleSameNameSkills moves a skill that both apps have only once instead of asking the user to
// pick. Identical copies are one skill: the first is brought over and the rest are shown as
// already covered. When they differ, the most recently edited copy is brought over and the others
// stay selectable as alternatives. Settled skills are removed from competing.
func (s *scanner) settleSameNameSkills(competing map[int]bool, sharedSkills map[string]bool) {
	groups := map[string][]int{}
	for index, item := range s.report.Items {
		if competing[index] && item.Kind == "skill" {
			groups[strings.ToLower(item.Name)] = append(groups[strings.ToLower(item.Name)], index)
		}
	}
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		fingerprints := map[string]bool{}
		newest, newestTime, tied := members[0], time.Time{}, false
		for _, index := range members {
			c := s.report.candidates[s.report.Items[index].ID]
			var files []string
			for _, op := range c.ops {
				rel, _ := filepath.Rel(filepath.Join(s.report.Options.Destination, "skills"), op.path)
				// The mode counts: a script that is executable in one copy only is a different skill.
				files = append(files, strings.ToLower(filepath.ToSlash(rel))+"\x00"+digest(op.content)+"\x00"+fmt.Sprint(op.mode&0o111 != 0))
			}
			sort.Strings(files)
			fingerprints[strings.Join(files, "\x00")] = true
			// Recency comes from the skill's own files. Its sources also include the app settings
			// read to check visibility, and editing those says nothing about the skill.
			folder := ""
			for source := range c.sources {
				if strings.EqualFold(filepath.Base(source), "SKILL.md") && (folder == "" || len(filepath.Dir(source)) < len(folder)) {
					folder = filepath.Dir(source)
				}
			}
			edited := time.Time{}
			for source := range c.sources {
				if folder == "" || !under(folder, source) {
					continue
				}
				if info, err := os.Stat(source); err == nil && info.ModTime().After(edited) {
					edited = info.ModTime()
				}
			}
			switch {
			case edited.After(newestTime):
				newest, newestTime, tied = index, edited, false
			case edited.Equal(newestTime):
				tied = true // a clone or a synced folder: the same time, so the first copy is kept
			}
		}
		keep := newest
		if len(fingerprints) == 1 {
			keep = members[0]
		}
		kept := &s.report.Items[keep]
		why := "was edited more recently"
		if tied {
			why = "comes first, since the copies were last changed at the same time"
		}
		// No copy is selected for you when one would replace a skill already in Copilot, or when the
		// shared ~/.agents/skills has one (reviewCompetingImports then holds both back).
		chosen := ", so it is the one selected. Select this one instead to use it, and clear the " + kept.Source + " copy."
		keptChosen := ", so it is selected. To use the other copy, select it under What needs work and clear this one."
		switch {
		case sharedSkills[strings.ToLower(kept.Name)]:
			chosen = ". A shared skill with this name is already available, so no copy is selected for you; select one only to use it instead."
			keptChosen = chosen
		case kept.Conflict:
			chosen = ". Only one copy can replace the skill already in Copilot; choose Replace on the one you want."
			keptChosen = chosen
		}
		var others []string
		for _, index := range members {
			competing[index] = false
			if index == keep {
				continue
			}
			item := &s.report.Items[index]
			others = append(others, item.Source)
			if len(fingerprints) == 1 {
				// Only the kept copy is written, so this one has no conflict or choice of its own.
				item.Selectable = false
				item.Conflict = false
				item.Disposition = "native"
				item.Detail = "Identical to the " + kept.Source + " copy of this skill, so only that copy needs to be brought over."
			} else {
				item.Disposition = "needs-review"
				item.Note = "The " + kept.Source + " copy of this skill differs and " + why + chosen
			}
		}
		if len(fingerprints) > 1 {
			kept.Note = "The " + strings.Join(others, " and ") + " copy of this skill differs; this one " + why + keptChosen
		}
	}
}

func (s *scanner) reviewCompetingImports() {
	owners := map[string]int{}
	competing := map[int]bool{}
	sharedSkills := map[string]bool{}
	mcpNames := map[string][]int{}
	for _, item := range s.report.Items {
		if item.Source == "shared" && item.Kind == "skill" && item.Disposition == "native" {
			sharedSkills[strings.ToLower(item.Name)] = true
		}
	}
	for index, item := range s.report.Items {
		if item.Kind == "mcp" {
			key := strings.ToLower(item.Name)
			mcpNames[key] = append(mcpNames[key], index)
		}
		if !item.Selectable {
			continue
		}
		for _, op := range s.report.candidates[item.ID].ops {
			if op.appendText != "" || op.patch != nil || op.pointers != nil {
				continue
			}
			// Windows and typical macOS volumes ignore case. Keep alternatives
			// manual even on a case-sensitive volume instead of guessing a winner.
			key := strings.ToLower(filepath.Clean(op.path))
			if previous, found := owners[key]; found && previous != index {
				competing[previous], competing[index] = true, true
			} else {
				owners[key] = index
			}
		}
	}
	s.settleSameNameSkills(competing, sharedSkills)
	for index := range s.report.Items {
		item := &s.report.Items[index]
		if !item.Selectable {
			continue
		}
		if competing[index] {
			item.Disposition = "needs-review"
			item.Detail += " Another import writes to the same file. Choose one alternative."
		}
		if item.Kind == "skill" && sharedSkills[strings.ToLower(item.Name)] {
			item.Disposition = "needs-review"
			item.Detail += " A shared skill with this name is already available. Review which version to use."
		}
		if item.Kind == "mcp" && len(mcpNames[strings.ToLower(item.Name)]) > 1 {
			item.Disposition = "needs-review"
			item.Detail += " Another server uses this name. Review its source and scope; existing definitions will not be replaced."
		}
	}
}

func passiveVersion(source, executable string) string {
	if executable == "" {
		return "Executable not found on PATH"
	}
	pkg := map[string]string{"copilot": "@github/copilot", "codex": "@openai/codex", "claude": "@anthropic-ai/claude-code"}[source]
	base := filepath.Dir(executable)
	for _, p := range []string{filepath.Join(base, "node_modules", filepath.FromSlash(pkg), "package.json"), filepath.Join(base, "..", "lib", "node_modules", filepath.FromSlash(pkg), "package.json")} {
		if m, e := readMap(p); e == nil && str(m["name"]) == pkg && str(m["version"]) != "" {
			return str(m["version"]) + " (package metadata; runtime not executed)"
		}
	}
	return "Native executable detected; version not executed during discovery"
}

func samePath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}
func (s *scanner) warn(v string) { s.report.Warnings = append(s.report.Warnings, v) }
func (s *scanner) snapshot(p string) (snapshot, error) {
	if v, ok := s.report.snapshots[p]; ok {
		return v, nil
	}
	b, m, e := readFile(p)
	v := snapshot{hash: "absent", mode: 0600}
	if e == nil {
		v = snapshot{digest(b), b, m}
	} else if !errors.Is(e, os.ErrNotExist) {
		return v, e
	}
	s.report.snapshots[p] = v
	return v, nil
}
func (s *scanner) add(i Item, c candidate) string {
	if c.sources == nil {
		c.sources = map[string]string{}
	}
	origins := make([]string, 0, len(c.sources))
	for p := range c.sources {
		origins = append(origins, p)
	}
	sort.Strings(origins)
	id := digest([]byte(i.Source + "\x00" + i.Kind + "\x00" + i.Name + "\x00" + i.Detail + "\x00" + strings.Join(origins, "\x00")))
	i.ID = id[:20]
	if _, found := s.report.candidates[i.ID]; found {
		return i.ID
	}
	for _, op := range c.ops {
		if !under(s.report.Options.Destination, op.path) {
			i.Selectable = false
			i.Disposition = "unsupported"
			i.Detail += " Destination is outside the selected profile."
			break
		}
		snap, e := s.snapshot(op.path)
		if e != nil {
			i.Selectable = false
			i.Disposition = "needs-review"
			i.Detail += " " + e.Error()
			continue
		}
		if snap.hash != "absent" {
			switch {
			case op.appendText != "":
				// Appending keeps the existing file; only a replacement needs approval.
				if !bytes.Contains(snap.content, []byte(op.appendText)) {
					i.Detail += " Your existing " + filepath.Base(op.path) + " is kept; a reference to the import is added at its end."
				}
			case op.pointers != nil:
				if !op.replace && pointersExist(snap.content, op.pointers) {
					i.Conflict = true
				}
			case op.patch != nil || !bytes.Equal(snap.content, op.content):
				i.Conflict = true
			}
		}
		if i.Destination == "" {
			i.Destination = op.path
		}
	}
	if c.activation != nil {
		i.ActivationKind = c.activation.Kind
	}
	s.report.Items = append(s.report.Items, i)
	s.report.candidates[i.ID] = c
	return i.ID
}
func (s *scanner) info(source, kind, name, disposition, detail string) {
	s.add(Item{Source: source, Kind: kind, Name: name, Disposition: disposition, Detail: detail}, candidate{})
}
func (s *scanner) source(p string, c *candidate) ([]byte, uint32, error) {
	b, m, e := readFile(p)
	if e == nil {
		if c.sources == nil {
			c.sources = map[string]string{}
		}
		c.sources[p] = digest(b)
	}
	return b, m, e
}

func (s *scanner) instructions(source, root, rel string, native bool) {
	path := filepath.Join(root, rel)
	if !exists(path) {
		return
	}
	if native {
		s.info(source, "instructions", filepath.Base(path), "native", "Already read from the selected Copilot profile; no duplicate copy.")
		return
	}
	c := candidate{}
	b, _, e := s.source(path, &c)
	if e != nil {
		s.warn(e.Error())
		return
	}
	i := Item{Source: source, Kind: "instructions", Name: filepath.Base(path), Disposition: "convertible", Selectable: true, Detail: "Review personal instructions and their relative includes. Imported text is explicit context, not a permission policy.", Preview: redactedPreview(b)}
	targetRoot := filepath.Join(s.report.Options.Destination, "imports", safeName(source))
	visited := map[string]bool{}
	var include func(string) error
	include = func(p string) error {
		if visited[p] {
			return nil
		}
		visited[p] = true
		if len(visited) > 40 {
			return errors.New("instruction include limit exceeded")
		}
		v, mode, err := s.source(p, &c)
		if err != nil {
			return err
		}
		r, err := filepath.Rel(root, p)
		if err != nil || !under(root, p) {
			return errors.New("instruction include escapes the source root")
		}
		if literalSecret(v) {
			return errors.New("credential-like text in " + filepath.ToSlash(r) + secretWhere(v) + " requires editing before import")
		}
		if externalReference.Match(v) {
			return errors.New("absolute or home-relative instruction references require manual adaptation")
		}
		c.ops = append(c.ops, operation{path: filepath.Join(targetRoot, r), content: v, mode: mode})
		for _, m := range importReference.FindAllSubmatch(v, -1) {
			ref := string(m[1])
			next := filepath.Clean(filepath.Join(filepath.Dir(p), filepath.FromSlash(ref)))
			if !under(root, next) {
				return errors.New("instruction include escapes the source root")
			}
			if !exists(next) {
				return fmt.Errorf("referenced instruction file is missing: %s", ref)
			}
			if err = include(next); err != nil {
				return err
			}
		}
		return nil
	}
	if e = include(path); e != nil {
		i.Selectable = false
		i.Disposition = "needs-review"
		i.Detail += " " + e.Error()
		c.ops = nil
	} else {
		includePath := filepath.ToSlash(filepath.Join("imports", safeName(source), rel))
		c.ops = append(c.ops, operation{path: filepath.Join(s.report.Options.Destination, "copilot-instructions.md"), appendText: "\n\n<!-- Copilot Ready: reviewed " + source + " instructions -->\n@" + includePath + "\n", mode: 0600})
	}
	s.add(i, c)
}

func (s *scanner) rules(source, root, dir string, native bool) {
	s.walkFiles(filepath.Join(root, dir), func(p string) {
		if !strings.HasSuffix(strings.ToLower(p), ".md") {
			return
		}
		if native {
			s.info(source, "rules", filepath.Base(p), "native", "Personal path-specific instructions already belong to this profile.")
			return
		}
		c := candidate{}
		b, _, e := s.source(p, &c)
		if e != nil {
			s.warn(e.Error())
			return
		}
		meta, body, e := frontmatter(b)
		if e != nil {
			s.info(source, "rules", filepath.Base(p), "needs-review", "Invalid instruction metadata.")
			return
		}
		apply := str(meta["applyTo"])
		if source == "claude" {
			if paths, ok := meta["paths"].([]any); ok {
				var globs []string
				for _, v := range paths {
					if str(v) != "" {
						globs = append(globs, str(v))
					}
				}
				apply = strings.Join(globs, ",")
			} else if p := str(meta["paths"]); p != "" {
				apply = p
			}
		}
		if apply == "" {
			apply = "**"
		}
		header, _ := yaml.Marshal(map[string]any{"applyTo": apply})
		content := append(append([]byte("---\n"), header...), []byte("---\n"+string(body))...)
		i := Item{Source: source, Kind: "rules", Name: filepath.Base(p), Disposition: "convertible", Selectable: true, Detail: "Convert paths to Copilot applyTo; review pattern semantics. References inside modular instructions are not expanded.", Preview: redactedPreview(content)}
		if literalSecret(b) {
			i.Selectable = false
			i.Disposition = "needs-review"
			i.Detail += " Credential-like text needs removing first" + secretWhere(b) + "."
		} else if importReference.Match(body) {
			i.Selectable = false
			i.Disposition = "needs-review"
			i.Detail += " File references need manual adaptation."
		}
		rel, _ := filepath.Rel(filepath.Join(root, dir), p)
		dest := filepath.Join(s.report.Options.Destination, "instructions", safeName(source), strings.TrimSuffix(rel, ".md")+".instructions.md")
		c.ops = []operation{{path: dest, content: content, mode: 0600}}
		s.add(i, c)
	})
}
func frontmatter(b []byte) (map[string]any, []byte, error) {
	text := strings.ReplaceAll(strings.TrimPrefix(string(b), "\ufeff"), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return map[string]any{}, b, nil
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return nil, nil, errors.New("unterminated frontmatter")
	}
	var meta map[string]any
	if e := yaml.Unmarshal([]byte(text[4:4+end]), &meta); e != nil {
		return nil, nil, e
	}
	return meta, []byte(strings.TrimPrefix(text[4+end+4:], "\n")), nil
}

// rawFrontmatter holds frontmatter values as written: top-level scalars, and lists of scalars
// joined with ", ", plus the same for the entries of a metadata map.
type rawFrontmatter struct{ top, metadata map[string]string }

func frontmatterText(b []byte) rawFrontmatter {
	raw := rawFrontmatter{top: map[string]string{}, metadata: map[string]string{}}
	text := strings.ReplaceAll(strings.TrimPrefix(string(b), "\ufeff"), "\r\n", "\n")
	end := strings.Index(text[min(4, len(text)):], "\n---")
	if !strings.HasPrefix(text, "---\n") || end < 0 {
		return raw
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text[4:4+end]), &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return raw
	}
	fill := func(m *yaml.Node, into map[string]string) {
		for i := 0; i+1 < len(m.Content); i += 2 {
			key, value := m.Content[i].Value, m.Content[i+1]
			switch value.Kind {
			case yaml.ScalarNode:
				if value.Tag != "!!null" {
					into[key] = value.Value
				}
			case yaml.SequenceNode:
				var parts []string
				for _, item := range value.Content {
					if item.Kind != yaml.ScalarNode || strings.Contains(item.Value, ",") {
						parts = nil // joined, "b, c" would read as two items
						break
					}
					parts = append(parts, item.Value)
				}
				if parts != nil {
					into[key] = strings.Join(parts, ", ")
				}
			}
		}
	}
	root := doc.Content[0]
	fill(root, raw.top)
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "metadata" && root.Content[i+1].Kind == yaml.MappingNode {
			fill(root.Content[i+1], raw.metadata)
		}
	}
	return raw
}

// copilotReadsSkill reports a skill folder inside a skills location Copilot reads itself: the
// shared ~/.agents/skills, or the skills of the profile setup writes to.
func (s *scanner) copilotReadsSkill(folder string) bool {
	for _, root := range []string{filepath.Join(s.report.Options.Home, ".agents", "skills"), filepath.Join(s.report.Options.Destination, "skills")} {
		if resolved, err := filepath.EvalSymlinks(root); err == nil && filepath.Dir(folder) == resolved {
			return true
		}
	}
	return false
}

// skillFile returns the folder's SKILL.md, whatever the case of its name, or "" when it has none.
func skillFile(folder string) string {
	// The folder's own listing gives the name as written, also on a case-insensitive disk, where
	// SKILL.md would be found for a skill.md too.
	entries, _ := os.ReadDir(folder)
	found := ""
	for _, entry := range entries {
		if entry.Name() == "SKILL.md" {
			return filepath.Join(folder, entry.Name())
		}
		if strings.EqualFold(entry.Name(), "SKILL.md") && !entry.IsDir() {
			found = filepath.Join(folder, entry.Name())
		}
	}
	return found
}

// frontmatterHeader writes meta as YAML in the original frontmatter's key order, keeping the text
// and comments of each value that didn't change. Keys that are new follow, sorted.
func frontmatterHeader(original []byte, meta map[string]any) []byte {
	fallback := func() []byte { header, _ := yaml.Marshal(meta); return header }
	text := strings.ReplaceAll(strings.TrimPrefix(string(original), "\ufeff"), "\r\n", "\n")
	end := strings.Index(text[min(4, len(text)):], "\n---")
	var doc yaml.Node
	if !strings.HasPrefix(text, "---\n") || end < 0 || yaml.Unmarshal([]byte(text[4:4+end]), &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fallback()
	}
	root, seen := doc.Content[0], map[string]bool{}
	// A value that shares another's text through an anchor (&who) and alias (*who) can't be kept as
	// written once the anchor's key moves, so with any of them every value is written out in full.
	shared := false
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.AliasNode || n.Anchor != "" {
			shared = true
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	var content []*yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		v, ok := meta[key.Value]
		if !ok || seen[key.Value] {
			continue
		}
		seen[key.Value] = true
		var was any
		if !shared && value.Decode(&was) == nil && reflect.DeepEqual(was, v) {
			content = append(content, key, value)
			continue
		}
		key.Anchor = ""
		var n yaml.Node
		if n.Encode(v) != nil {
			return fallback()
		}
		n.LineComment = value.LineComment
		content = append(content, key, &n)
	}
	for _, k := range sortedKeys(meta) {
		if seen[k] {
			continue
		}
		var n yaml.Node
		if n.Encode(meta[k]) != nil {
			return fallback()
		}
		content = append(content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &n)
	}
	root.Content = content
	header, err := yaml.Marshal(&doc)
	if err != nil {
		return fallback()
	}
	return header
}

func (s *scanner) walkFiles(root string, fn func(string)) {
	if !exists(root) {
		return
	}
	if e := noLinks(root); e != nil {
		s.warn(e.Error())
		return
	}
	count := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			s.warn("Cannot inspect " + p)
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			s.warn("Linked customization skipped: " + p)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "__pycache__" || exists(filepath.Join(p, "pyvenv.cfg")) {
				// Dependency caches and Python virtual environments are rebuilt, not copied.
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" || d.Name() == "Thumbs.db" || strings.EqualFold(d.Name(), "desktop.ini") || strings.HasPrefix(d.Name(), "._") {
			return nil // folder view settings, not content
		}
		count++
		if count > 1500 {
			s.warn("Customization inventory limit reached in " + root)
			return fs.SkipAll
		}
		fn(p)
		return nil
	})
}

func (s *scanner) skills(source, root string, native bool) {
	if !exists(root) {
		return
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		s.warn("Cannot inspect " + root)
		return
	}
	for _, entry := range entries {
		folder := filepath.Join(root, entry.Name())
		path := skillFile(folder)
		if entry.Type()&os.ModeSymlink != 0 {
			// Links are never followed when copying (see walkFiles), but a linked skill is still listed.
			if info, err := os.Stat(folder); err == nil && info.IsDir() && path != "" {
				target, _ := filepath.EvalSymlinks(folder)
				if !native && s.copilotReadsSkill(target) {
					// npx skills keeps the folder in ~/.agents/skills and links it from each app.
					s.info(source, "skill", entry.Name(), "native", "A link to "+target+", which Copilot reads itself; no copy is made.")
				} else if native {
					s.info(source, "skill", entry.Name(), "native", "A linked folder ("+target+") in a location Copilot reads itself; no copy is made. Check in Copilot that it appears.")
				} else {
					s.info(source, "skill", entry.Name(), "needs-review", "This skill folder is a link to "+target+", and Copilot Ready doesn't copy through links. Copy the folder into place, or add it to Copilot yourself.")
				}
			}
			continue
		}
		if !entry.IsDir() || path == "" {
			continue
		}
		if native {
			detail := "Discovered natively in this profile or the shared ~/.agents/skills directory; no duplicate import."
			name := entry.Name()
			if v, _, err := readFile(path); err == nil {
				if meta, _, err := frontmatter(v); err == nil {
					if value := str(meta["name"]); namePattern.MatchString(value) {
						name = value
					}
					if _, ok := meta["allowed-tools"]; ok {
						detail += " This native skill contains tool pre-approvals; they remain in the original file and need separate review."
						s.warn(detail)
					}
				}
			}
			settingsSource := ""
			switch filepath.Base(filepath.Dir(root)) {
			case ".agents":
				settingsSource = "codex"
			case ".claude":
				settingsSource = "claude"
			}
			if settingsSource != "" {
				if reason := s.skillSettings(settingsSource, folder, name, &candidate{}); reason != "" {
					detail += " " + reason + " Copilot discovers this folder independently; no source setting was changed."
					s.warn(detail)
				}
				if v, _, err := readFile(filepath.Join(folder, "agents", "openai.yaml")); err == nil {
					var metadata map[string]any
					if yaml.Unmarshal(v, &metadata) != nil || mapValue(metadata["policy"])["allow_implicit_invocation"] == false || len(mapValue(metadata["dependencies"])) > 0 {
						detail += " Codex-only policy or dependencies in agents/openai.yaml need review; Copilot may discover the shared skill without those restrictions."
						s.warn(detail)
					}
				}
			}
			s.info(source, "skill", name, "native", detail)
			continue
		}
		c := candidate{}
		b, _, e := s.source(path, &c)
		if e != nil {
			s.warn(e.Error())
			continue
		}
		original := b // rewriting the frontmatter can drop the quotes a credential is judged by
		meta, body, e := frontmatter(b)
		name := str(meta["name"])
		if name == "" {
			name = entry.Name()
		}
		i := Item{Source: source, Kind: "skill", Name: name, Disposition: "convertible", Selectable: true, Detail: "Imports skill instructions and supporting files, excluding dependency caches. Review executable files and required tools before use."}
		block := func(reason string) { i.Selectable = false; i.Disposition = "needs-review"; i.Detail += " " + reason }
		if e != nil || !namePattern.MatchString(name) || len(name) > 64 || str(meta["description"]) == "" || len(str(meta["description"])) > 1024 {
			block("A valid name (up to 64 characters) and description (up to 1024 characters) are required.")
		}
		raw := frontmatterText(b)
		if existing, ok := meta["metadata"].(map[string]any); ok {
			for k, v := range raw.metadata {
				existing[k] = v // as written, so metadata.version 1.10 stays "1.10"
			}
		}
		for _, key := range sortedKeys(meta) {
			switch key {
			case "name", "description", "license", "compatibility", "metadata", "argument-hint":
			case "disable-model-invocation", "user-invocable":
				if _, ok := meta[key].(bool); !ok {
					block("Invocation flags must be explicit true/false values.")
				}
			case "allowed-tools":
				delete(meta, key)
				i.Detail += " Tool pre-approvals are removed; grant any required permissions separately in Copilot."
			case "version", "author", "authors", "tags", "keywords", "homepage", "repository", "category", "icon", "color", "paths":
				// Descriptive fields belong under metadata in the Agent Skills format, whose values are
				// text. They keep the text as written, so version 1.10 stays "1.10". Cursor's display
				// and file-matching keys (icon, color, paths) move there too; other apps' don't.
				if source != "cursor" && (key == "icon" || key == "color" || key == "paths") {
					block("Source-specific skill metadata requires adaptation: " + key + ".")
					continue
				}
				extra, ok := meta["metadata"].(map[string]any)
				if meta["metadata"] != nil && !ok {
					block("Source-specific skill metadata requires adaptation: " + key + ".")
					continue
				}
				var moved any
				text, ok := raw.top[key]
				switch value := meta[key].(type) {
				case nil:
					moved, text, ok = nil, "null", true // kept as null, not the text "null"
				case []any:
					moved = text
					if !ok && slices.IndexFunc(value, func(v any) bool { _, isText := v.(string); return !isText }) < 0 {
						moved, text, ok = value, fmt.Sprint(value), true // an item with a comma: kept as a list
					}
				default:
					moved = text
				}
				if !ok {
					block("Skill metadata must be text or a list of text: " + key + ".")
					continue
				}
				if extra == nil {
					extra = map[string]any{}
				}
				if prior, taken := extra[key]; !taken {
					extra[key] = moved
				} else if fmt.Sprint(prior) != text {
					i.Detail += " Its " + key + " (" + redactSecrets(text) + ") differs from metadata." + key + " (" + redactSecrets(fmt.Sprint(prior)) + "), which is kept."
				}
				meta["metadata"] = extra
				delete(meta, key)
				if key == "paths" {
					i.Detail += " Cursor offers it only for files matching its paths; Copilot offers it everywhere."
				}
			default:
				block("Source-specific skill metadata requires adaptation: " + key + ".")
			}
		}
		if reason := s.skillSettings(source, folder, name, &c); reason != "" {
			block(reason)
		}
		openAI := filepath.Join(folder, "agents", "openai.yaml")
		if exists(openAI) {
			v, _, err := s.source(openAI, &c)
			var metadata map[string]any
			if err != nil || yaml.Unmarshal(v, &metadata) != nil {
				block("Cannot validate agents/openai.yaml.")
			} else {
				if value, ok := metadata["policy"]; ok && mapValue(value) == nil {
					block("Cannot interpret Codex invocation policy.")
				}
				if policy := mapValue(metadata["policy"]); policy != nil {
					for key, value := range policy {
						if key != "allow_implicit_invocation" {
							block("Codex invocation policy requires adaptation.")
							continue
						}
						if enabled, ok := value.(bool); !ok {
							block("Codex invocation policy must be true/false.")
						} else if !enabled {
							if meta == nil {
								meta = map[string]any{}
							}
							meta["disable-model-invocation"] = true
							i.Detail += " Codex's manual-only invocation is preserved."
						}
					}
				}
				if dependencies, ok := metadata["dependencies"]; ok && (mapValue(dependencies) == nil || len(mapValue(dependencies)) > 0) {
					block("Codex tool dependencies need explicit mapping before this skill can be imported.")
				}
			}
		}
		if meta != nil {
			meta["name"] = name
			b = append(append([]byte("---\n"), frontmatterHeader(original, meta)...), append([]byte("---\n"), body...)...)
		}
		if secretFile("SKILL.md", b) || literalSecret(original) {
			block("Credential-like content needs removing first" + secretWhere(original) + ".")
			i.Preview = "[Sensitive file contents omitted; review locally before import.]"
		} else {
			i.Preview = redactedPreview(b)
		}
		if suspiciousRuntime(b) {
			i.Selectable = false
			i.Disposition = "unsupported"
			i.Detail += " Refers to client-specific tools or dynamic command evaluation."
		}
		warnings := len(s.report.Warnings)
		s.walkFiles(folder, func(p string) {
			v, m, err := s.source(p, &c)
			if err != nil {
				block(err.Error())
				return
			}
			rel, _ := filepath.Rel(folder, p)
			shown := v // what the file preview is built from
			if strings.EqualFold(rel, "SKILL.md") {
				v, rel, shown = b, "SKILL.md", b
				if literalSecret(original) {
					shown = original
				}
			}
			if sensitiveFile(rel) {
				block("A file named like this usually holds credentials, so it must be removed before import: " + filepath.ToSlash(rel) + ".")
			} else if literalSecret(v) || literalSecret(shown) {
				block("Credential-like text must be removed before import: " + filepath.ToSlash(rel) + secretWhere(shown) + ".")
			}
			if strings.HasPrefix(filepath.ToSlash(rel), ".claude-plugin/") || rel == ".mcp.json" || filepath.ToSlash(rel) == "hooks/hooks.json" {
				block("Bundled plugin or integration configuration needs separate migration.")
			}
			if utf8.Valid(v) && suspiciousRuntime(v) {
				block("Supporting file references client-specific behavior: " + filepath.ToSlash(rel) + ".")
			}
			if strings.EqualFold(filepath.Ext(rel), ".md") {
				if err := skillReferences(folder, p, v); err != nil {
					block(err.Error())
				}
			}
			file := previewFile(rel, shown, m)
			file.Size = int64(len(v))
			i.Files = append(i.Files, file)
			c.ops = append(c.ops, operation{path: filepath.Join(s.report.Options.Destination, "skills", name, rel), content: v, mode: m})
		})
		if len(s.report.Warnings) != warnings {
			block("Some supporting files could not be inspected; resolve the discovery warning before import.")
		}
		s.add(i, c)
	}
}

func (s *scanner) skillSettings(source, folder, name string, c *candidate) string {
	var files []string
	if source == "codex" {
		files = []string{filepath.Join(s.report.Options.CodexHome, "config.toml")}
		for _, directory := range projectDirectories(s.report.Options.Project) {
			files = append(files, filepath.Join(directory, ".codex", "config.toml"))
		}
	} else if source == "claude" {
		files = []string{filepath.Join(s.report.Options.ClaudeHome, "settings.json")}
		for _, directory := range projectDirectories(s.report.Options.Project) {
			files = append(files, filepath.Join(directory, ".claude", "settings.json"), filepath.Join(directory, ".claude", "settings.local.json"))
		}
	}
	for _, p := range files {
		if !exists(p) {
			if c.sources == nil {
				c.sources = map[string]string{}
			}
			c.sources[p] = "absent"
			continue
		}
		b, _, err := s.source(p, c)
		if err != nil {
			return "Cannot inspect skill visibility settings; review them before import."
		}
		if source == "codex" {
			var config struct {
				Skills struct {
					Config []struct {
						Path    string
						Enabled *bool
					}
				}
			}
			if toml.Unmarshal(b, &config) != nil {
				return "Cannot parse Codex skill visibility settings."
			}
			for _, entry := range config.Skills.Config {
				path := entry.Path
				if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
					path = filepath.Join(s.report.Options.Home, path[2:])
				}
				if !filepath.IsAbs(path) {
					path = filepath.Join(filepath.Dir(p), path)
				}
				if entry.Enabled != nil && !*entry.Enabled && (samePath(path, folder) || samePath(path, filepath.Join(folder, "SKILL.md"))) {
					return "A disabled entry was found in Codex settings; review its effective state before enabling this skill in Copilot."
				}
			}
		} else {
			config, err := parseJSON(b)
			if err != nil {
				return "Cannot parse Claude skill visibility settings."
			}
			if override, ok := mapValue(config["skillOverrides"])[name]; ok && override != "on" {
				return "Claude settings restrict this skill's visibility or invocation; adapt that choice before import."
			}
			denied, _ := mapValue(config["permissions"])["deny"].([]any)
			for _, value := range denied {
				rule := str(value)
				if rule == "Skill" {
					return "Claude settings deny skill use; no permission is imported."
				}
				if strings.HasPrefix(rule, "Skill(") && strings.HasSuffix(rule, ")") {
					pattern := strings.TrimSuffix(strings.TrimPrefix(rule, "Skill("), ")")
					if match, _ := filepath.Match(pattern, name); match {
						return "Claude settings deny this skill; no permission is imported."
					}
				}
			}
		}
	}
	return ""
}

var skillLinks = regexp.MustCompile(`\[[^\]\n]*\]\(([^\s)]+)\)`)

func secretFile(path string, content []byte) bool {
	return sensitiveFile(path) || literalSecret(content)
}

// skillReferences checks a skill's Markdown links. Skills have no @file imports, so "@scope/pkg"
// and "@decorator" text is not a reference. A rooted "/path" link counts only when that file exists;
// otherwise it is a website path copied from documentation.
func skillReferences(folder, file string, content []byte) error {
	var references []string
	for _, match := range skillLinks.FindAllSubmatch(content, -1) {
		references = append(references, string(match[1]))
	}
	for _, ref := range references {
		if len(ref) > 2 && ref[1] == ':' && (ref[2] == '/' || ref[2] == '\\') {
			return errors.New("An absolute skill file reference needs adaptation before import.")
		}
		if strings.HasPrefix(ref, "/") {
			// A file on this computer is one the skill needs and doesn't carry. Anything else, a
			// folder such as / or a missing path, is a website path copied from documentation.
			if info, err := os.Stat(ref); err == nil && !info.IsDir() {
				return errors.New("An absolute skill file reference needs adaptation before import.")
			}
			continue
		}
		u, err := url.Parse(ref)
		if err != nil {
			return errors.New("A skill file reference cannot be parsed; review it before import.")
		}
		if u.Scheme == "file" {
			return errors.New("A local file URL needs adaptation before import.")
		}
		if u.IsAbs() || u.Host != "" || u.Path == "" {
			continue
		}
		path := filepath.FromSlash(u.Path)
		if filepath.IsAbs(path) || strings.HasPrefix(path, "~") {
			return errors.New("An absolute skill file reference needs adaptation before import.")
		}
		target := filepath.Clean(filepath.Join(filepath.Dir(file), path))
		if !under(folder, target) || !exists(target) {
			return errors.New("A skill file reference is missing or leaves its folder; adapt it before import.")
		}
		if filepath.Dir(target) == folder && strings.EqualFold(filepath.Base(path), "SKILL.md") && filepath.Base(path) != "SKILL.md" {
			// The skill file is always written as SKILL.md, where another spelling won't resolve on a
			// case-sensitive disk.
			return errors.New("A link to " + filepath.Base(path) + " must say SKILL.md, the name the skill file is copied as; change it before import.")
		}
		rel, _ := filepath.Rel(folder, target)
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == "node_modules" || part == ".git" || part == "__pycache__" {
				return errors.New("A skill references an excluded dependency/cache folder; adapt it before import.")
			}
		}
		// Python virtual environments are not copied either (see walkFiles).
		for dir := target; under(folder, dir) && dir != folder; dir = filepath.Dir(dir) {
			if exists(filepath.Join(dir, "pyvenv.cfg")) {
				return errors.New("A skill references an excluded dependency/cache folder; adapt it before import.")
			}
		}
	}
	return nil
}

func previewFile(rel string, content []byte, mode uint32) FilePreview {
	file := FilePreview{Path: filepath.ToSlash(rel), Size: int64(len(content)), Executable: mode&0111 != 0, Text: utf8.Valid(content) && !bytes.ContainsRune(content, '\x00')}
	// Windows and interpreter-driven scripts do not need a POSIX executable bit.
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".exe", ".cmd", ".bat", ".ps1", ".sh", ".py", ".js", ".mjs", ".cjs":
		file.Executable = true
	}
	if !file.Text {
		return file
	}
	// Files commonly containing credentials are metadata-only, even when a
	// token's format is unknown. Other text has credential-like values redacted.
	if secretFile(rel, content) {
		file.Preview = "[Sensitive file contents omitted; review locally before import.]"
		return file
	}
	value := redactSecrets(string(content))
	if len(value) > 4096 {
		value = value[:4096]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		file.Truncated = true
	}
	file.Preview = value
	return file
}

func sensitiveFile(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	switch {
	case name == ".env.example" || name == ".env.sample" || name == ".env.template" || name == ".env.dist":
		return false // a template, judged by what it holds
	case strings.HasPrefix(strings.TrimPrefix(name, "."), "credentials") && (strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".txt")):
		return false // documentation about credentials, such as credentials-setup.md
	}
	return name == ".env" || strings.HasPrefix(name, ".env.") || strings.HasPrefix(strings.TrimPrefix(name, "."), "credentials") || name == "auth.json" || name == "tokens.json" || name == "token.json" || name == "secrets.json" || name == "secrets.yaml" || name == ".npmrc" || name == ".pypirc" || name == ".netrc" || strings.HasPrefix(name, "id_rsa") || strings.HasPrefix(name, "id_ed25519") || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key") || strings.HasSuffix(name, ".pfx") || strings.HasSuffix(name, ".p12")
}

// bangCommand is Claude's shell command run while loading a skill: inline "!`command`" at the
// start of a line or after a space, bracket or quote, or a fenced block opened with "```!".
// "…${name}!`" closing a JavaScript template string is not one.
var bangCommand = regexp.MustCompile("(?m)(?:^|[\\s(\\[>\"'])!`|^[ \\t]*```!")

func suspiciousRuntime(b []byte) bool {
	v := strings.ToLower(string(b))
	for _, x := range []string{"mcp__", "node_repl", "functions.exec", "plugin://", "app://", "$arguments", "${claude_", "$claude_", "${codex"} {
		if strings.Contains(v, x) {
			return true
		}
	}
	return bangCommand.Match(b)
}
func (s *scanner) agents(source, root string, native bool) {
	s.walkFiles(root, func(p string) {
		if !strings.HasSuffix(p, ".md") {
			return
		}
		name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".md"), ".agent")
		if native {
			s.info(source, "agent", name, "native", "Already installed in the selected profile.")
			return
		}
		c := candidate{}
		b, _, e := s.source(p, &c)
		if e != nil {
			s.warn(e.Error())
			return
		}
		meta, body, e := frontmatter(b)
		i := Item{Source: source, Kind: "agent", Name: name, Disposition: "convertible", Selectable: true, Detail: "Imports agent purpose and body. Source model, permissions and client-specific metadata are not assumed equivalent.", Preview: redactedPreview(b)}
		if literalSecret(b) {
			i.Selectable = false
			i.Disposition = "needs-review"
			i.Detail += " Credential-like text needs removing first" + secretWhere(b) + "."
		} else if e != nil || !namePattern.MatchString(name) || str(meta["description"]) == "" || suspiciousRuntime(b) {
			i.Selectable = false
			i.Disposition = "needs-review"
			i.Detail += " Metadata or tool references need review."
		}
		if readonly, _ := meta["readonly"].(bool); readonly && source == "cursor" {
			i.Selectable = false
			i.Disposition = "needs-review"
			i.Detail += " This agent is read-only in Cursor. Copilot has no such setting, so it could change files; limit its tools in Copilot before using it."
		}
		if background, _ := meta["is_background"].(bool); background && source == "cursor" {
			i.Detail += " Cursor runs it in the background; Copilot runs it like any other agent."
		}
		out := map[string]any{"name": name, "description": meta["description"]}
		if source == "copilot" {
			for _, k := range []string{"tools", "model", "include-custom-instructions", "infer"} {
				if v, ok := meta[k]; ok {
					out[k] = v
				}
			}
		} else if v, ok := meta["tools"]; ok {
			if !validAgentTools(v) {
				i.Selectable = false
				i.Disposition = "needs-review"
				i.Detail += " Tool allowlist is client-specific and must be mapped explicitly."
			} else {
				out["tools"] = v
			}
		}
		header, _ := yaml.Marshal(out)
		c.ops = []operation{{path: filepath.Join(s.report.Options.Destination, "agents", name+".agent.md"), content: append(append([]byte("---\n"), header...), append([]byte("---\n"), body...)...), mode: 0600}}
		s.add(i, c)
	})
}
func validAgentTools(v any) bool {
	a, ok := v.([]any)
	if !ok {
		return false
	}
	for _, x := range a {
		switch str(x) {
		case "read", "search", "edit", "execute", "web":
		default:
			return false
		}
	}
	return true
}
func (s *scanner) prompts(source, root string, native bool) {
	s.walkFiles(root, func(p string) {
		if !strings.HasSuffix(p, ".md") {
			return
		}
		name := strings.TrimSuffix(filepath.Base(p), ".md")
		c := candidate{}
		b, _, e := s.source(p, &c)
		if e != nil {
			s.warn(e.Error())
			return
		}
		meta, body, e := frontmatter(b)
		i := Item{Source: source, Kind: "prompt", Name: name, Disposition: "convertible", Selectable: true, Detail: "Converts a static prompt into a user-invoked skill. It will not run automatically.", Preview: redactedPreview(b)}
		if literalSecret(b) {
			i.Selectable = false
			i.Disposition = "needs-review"
			i.Detail += " Credential-like text needs removing first" + secretWhere(b) + "."
		} else if e != nil || suspiciousRuntime(b) || strings.Contains(string(body), "$1") {
			i.Selectable = false
			i.Disposition = "unsupported"
			i.Detail += " Dynamic arguments, shell expansion or client-specific references need a deliberate rewrite."
		}
		desc := str(meta["description"])
		if desc == "" {
			desc = "Manually invoke the imported " + name + " prompt"
		}
		header, _ := yaml.Marshal(map[string]any{"name": safeName(source + "-" + name), "description": desc, "user-invocable": true, "disable-model-invocation": true})
		content := append(append([]byte("---\n"), header...), append([]byte("---\n"), body...)...)
		c.ops = []operation{{path: filepath.Join(s.report.Options.Destination, "skills", safeName(source+"-"+name), "SKILL.md"), content: content, mode: 0600}}
		s.add(i, c)
	})
}

func (s *scanner) settings(source, path string, native bool) {
	if !exists(path) {
		return
	}
	c := candidate{}
	b, _, e := s.source(path, &c)
	if e != nil {
		s.warn(e.Error())
		return
	}
	m, e := parseJSON(b)
	if e != nil {
		s.info(source, "settings", source+" settings", "needs-review", "Cannot parse settings; original file remains untouched.")
		return
	}
	if h, ok := m["hooks"]; ok {
		s.hooks(source, path, h, native)
	}
	if p, ok := m["enabledPlugins"]; ok {
		s.pluginEntries(source, path, p, native)
	}
	if source != "copilot" {
		var keys []string
		for _, k := range []string{"model", "effortLevel", "permissions", "defaultMode", "permissionMode", "sandbox", "env"} {
			if _, ok := m[k]; ok {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			s.info(source, "settings", source+" preferences", "needs-review", "Detected "+strings.Join(keys, ", ")+". Model IDs, permissions and sandbox policies require explicit target choices; no automatic mapping.")
		}
		return
	}
	if native {
		s.info(source, "settings", "Copilot preferences", "native", "Existing settings, comments and unknown keys remain in place. Review the setup's proposed settings changes separately.")
		return
	}
	patch := map[string]any{}
	for _, k := range []string{"model", "autoTier", "effortLevel", "theme", "renderMarkdown", "mouse", "screenReader", "askUser", "includeCoAuthoredBy"} {
		if v, ok := m[k]; ok {
			patch[k] = v
		}
	}
	if len(patch) > 0 {
		c.ops = []operation{{path: filepath.Join(s.report.Options.Destination, "settings.json"), patch: patch, mode: 0600}}
		s.add(Item{Source: source, Kind: "settings", Name: "Copilot preferences", Disposition: "convertible", Selectable: true, Detail: "Imports supported Copilot presentation/model preferences only. Permissions, auth, trust, plugins and hooks are reviewed separately.", Preview: redactedPreview(encode(patch))}, c)
	}
	if _, ok := m["defaultPermissionMode"]; ok {
		s.info(source, "permissions", "Saved permission mode", "needs-review", "Existing approval preferences are not copied into a new profile. Review manual approvals in setup.")
	}
}

func (s *scanner) codexConfig(root string) {
	p := filepath.Join(root, "config.toml")
	if !exists(p) {
		return
	}
	b, _, e := readFile(p)
	if e != nil {
		s.warn(e.Error())
		return
	}
	var m map[string]any
	if e = toml.Unmarshal(b, &m); e != nil {
		s.info("codex", "settings", "Codex configuration", "needs-review", "Invalid TOML; no settings were imported.")
		return
	}
	if v := mapValue(m["mcp_servers"]); v != nil {
		s.mcpEntries("codex", p, v, false)
	}
	if h, ok := m["hooks"]; ok {
		s.hooks("codex", p, h, false)
	}
	if pset, ok := m["plugins"]; ok {
		s.pluginEntries("codex", p, pset, false)
	}
	for _, k := range []string{"model", "model_reasoning_effort", "approval_policy", "sandbox_mode", "notify", "profiles", "agents"} {
		if _, ok := m[k]; ok {
			s.info("codex", "settings", k, "needs-review", "Detected Codex "+k+". Preserve in Codex; choose supported Copilot behavior separately.")
		}
	}
}
func (s *scanner) mcpFile(source, p string, native bool) {
	if !exists(p) {
		return
	}
	m, e := readMap(p)
	if e != nil {
		s.warn("Cannot parse MCP inventory in " + p)
		return
	}
	if entries := mapValue(m["mcpServers"]); entries != nil {
		s.mcpEntries(source, p, entries, native)
	} else if source == "project" {
		// Copilot also accepts a bare server-name map at project scope.
		entries := map[string]any{}
		for name, value := range m {
			entry := mapValue(value)
			if entry != nil && (str(entry["command"]) != "" || str(entry["url"]) != "") {
				entries[name] = entry
			}
		}
		s.mcpEntries(source, p, entries, native)
	}
	if source == "claude" {
		if projects := mapValue(m["projects"]); projects != nil && s.report.Options.Project != "" {
			for key, val := range projects {
				matched := false
				for _, directory := range projectDirectories(s.report.Options.Project) {
					matched = matched || samePath(key, directory)
				}
				if matched {
					if servers := mapValue(mapValue(val)["mcpServers"]); servers != nil {
						s.mcpEntries("claude-project", p, servers, false)
					}
					if disabled, ok := mapValue(val)["disabledMcpServers"].([]any); ok {
						for _, value := range disabled {
							for n := range s.report.Items {
								item := &s.report.Items[n]
								if item.Kind == "mcp" && strings.HasPrefix(item.Source, "claude") && item.Name == str(value) {
									item.Selectable, item.Disposition = false, "needs-review"
									item.Detail += " Disabled in Claude for this project; it will not be enabled by migration."
									delete(s.report.candidates, item.ID)
								}
							}
						}
					}
				}
			}
		}
	}
}
func (s *scanner) mcpEntries(source, p string, entries map[string]any, native bool) {
	for _, name := range sortedKeys(entries) {
		if native {
			disposition := "native"
			detail := "Existing MCP definition in " + p + " remains in place; no connection attempted during discovery. Project definitions apply after folder trust and may override personal definitions."
			if source == "project" && filepath.Base(p) == ".mcp.json" {
				if reason := s.claudeMCPRestrictions(name, &candidate{}); reason != "" {
					disposition = "needs-review"
					detail += " " + reason + " Copilot may discover this file independently; review it before opening this project."
				}
			}
			// Earlier setups wrote "tools": [] for servers that signed in through Copilot, which
			// leaves them connected with no tools at all.
			if tools, ok := mapValue(entries[name])["tools"].([]any); ok && len(tools) == 0 && source == "copilot" {
				c := candidate{}
				if _, _, e := s.source(p, &c); e == nil {
					c.ops = []operation{{path: p, pointers: map[string]any{"/mcpServers/" + jsonPointer(name) + "/tools": []string{"*"}}, replace: true, mode: 0600}}
					s.add(Item{Source: source, Kind: "mcp", Name: name, Disposition: "convertible", Selectable: true, Detail: "This server allows no tools, so Copilot cannot use it. Allow all of its tools."}, c)
					continue
				}
			}
			s.info(source, "mcp", name, disposition, detail)
			continue
		}
		c := candidate{}
		_, _, e := s.source(p, &c)
		if e != nil {
			continue
		}
		cfg := mapValue(entries[name])
		i := Item{Source: source, Kind: "mcp", Name: name, Disposition: "convertible", Selectable: true, Detail: "Adds this server to Copilot's MCP configuration with all of its tools, as in the source app. It starts when Copilot needs it."}
		var cursorReasons []string
		if strings.HasPrefix(source, "cursor") && cfg != nil {
			cfg, cursorReasons = cursorMCP(cfg, s.report.Options.Home)
		}
		out, reasons := convertMCP(source, name, cfg)
		reasons = append(cursorReasons, reasons...)
		if strings.HasPrefix(source, "claude") {
			if reason := s.claudeMCPRestrictions(name, &c); reason != "" {
				reasons = append(reasons, reason)
			}
		}
		if source == "claude-project" || source == "cursor-project" {
			reasons = append(reasons, "This definition belongs to the selected project. Keep that scope; adding it to every project requires a separate manual decision.")
		}
		if len(reasons) > 0 {
			i.Selectable = false
			i.Disposition = "needs-review"
			i.Detail += " " + strings.Join(reasons, " ")
		}
		if strings.Contains(strings.ToLower(name), "node_repl") {
			i.Selectable = false
			i.Disposition = "unsupported"
			i.Detail = "Codex node_repl is a client-runtime integration. Portability is unproven; retain it in Codex."
		}
		if i.Selectable {
			if out["type"] == "local" && out["cwd"] == nil && s.report.Options.Project != "" {
				out["cwd"] = s.report.Options.Project
				i.Detail += " Runs from the project chosen in setup. Review relative file arguments before connecting."
			}
			if out["type"] == "http" || out["type"] == "sse" {
				i.Detail += " Saved sign-ins stay in the source app; if the server needs one, Copilot asks when it connects."
			}
			missing := map[string]bool{}
			for _, field := range []string{"env", "headers"} {
				for _, value := range mapValue(out[field]) {
					for _, ref := range envTemplate.FindAllStringSubmatch(str(value), -1) {
						if os.Getenv(ref[1]) == "" {
							missing[ref[1]] = true
						}
					}
				}
			}
			if len(missing) > 0 {
				names := []string{}
				for name := range missing {
					names = append(names, name)
				}
				sort.Strings(names)
				i.Disposition = "needs-review"
				i.Detail += " Missing environment variables: " + strings.Join(names, ", ") + ". Set them in the environment used to open Copilot; only references are saved."
			}
			if out["type"] == "sse" {
				i.Disposition = "needs-review"
				i.Detail += " This older connection type must be tested in Copilot."
			}
		}
		i.Preview = redactedPreview(encode(out))
		if i.Selectable {
			// Copilot enables every tool when "tools" is absent; an empty list would enable none.
			c.ops = []operation{{path: filepath.Join(s.report.Options.Destination, "mcp-config.json"), pointers: map[string]any{"/mcpServers/" + jsonPointer(name): out}, mode: 0600}}
		}
		s.add(i, c)
	}
}

// Read restrictions without copying permissions or credentials. Treat an explicit
// denial conservatively; launch-time overrides and managed policy remain native.
func (s *scanner) claudeMCPRestrictions(name string, c *candidate) string {
	files := []string{filepath.Join(s.report.Options.ClaudeHome, "settings.json")}
	for _, root := range projectDirectories(s.report.Options.Project) {
		files = append(files, filepath.Join(root, ".claude", "settings.json"), filepath.Join(root, ".claude", "settings.local.json"))
	}
	for _, p := range files {
		if !exists(p) {
			if c.sources == nil {
				c.sources = map[string]string{}
			}
			c.sources[p] = "absent"
			continue
		}
		b, _, err := s.source(p, c)
		if err != nil {
			return "Claude MCP restrictions could not be inspected; review them before import."
		}
		settings, err := parseJSON(b)
		if err != nil {
			return "Claude settings could not be parsed; review them before import."
		}
		for _, key := range []string{"disabledMcpServers", "disabledMcpjsonServers"} {
			values, _ := settings[key].([]any)
			for _, value := range values {
				if str(value) == name {
					return "Claude settings disable this server. That restriction is not portable; review it before using Copilot."
				}
			}
		}
		denied, _ := mapValue(settings["permissions"])["deny"].([]any)
		for _, value := range denied {
			rule := str(value)
			server := "mcp__" + name
			whole, _ := filepath.Match(rule, server)
			tools, _ := filepath.Match(rule, server+"__*")
			if rule == "mcp" || whole || tools || strings.HasPrefix(rule, server+"__") {
				return "Claude denies tools from this server. Adapt those restrictions in Copilot before importing; source permissions are not copied."
			}
		}
	}
	return ""
}

func (s *scanner) hookFiles(source, root string, native bool) {
	for _, p := range []string{filepath.Join(root, "hooks.json"), filepath.Join(root, "hooks", "hooks.json")} {
		if m, e := readMap(p); e == nil {
			if h, ok := m["hooks"]; ok {
				s.hooks(source, p, h, native)
			}
		}
	}
	if source == "copilot" {
		entries, _ := os.ReadDir(filepath.Join(root, "hooks"))
		for _, d := range entries {
			if d.IsDir() || d.Name() == "hooks.json" || !strings.HasSuffix(d.Name(), ".json") {
				continue
			}
			p := filepath.Join(root, "hooks", d.Name())
			if m, e := readMap(p); e == nil {
				if h, ok := m["hooks"]; ok {
					s.hooks(source, p, h, native)
				}
			}
		}
	}
}

// copilotHookEvents are the event names Copilot CLI documents, in its own camelCase and the
// PascalCase forms it accepts from Claude-style configuration.
var copilotHookEvents = map[string]bool{}

func init() {
	for _, e := range []string{"agentStop", "errorOccurred", "notification", "permissionRequest", "postToolUse", "postToolUseFailure", "preCompact", "preToolUse", "sessionEnd", "sessionStart", "subagentStart", "subagentStop", "userPromptSubmitted", "userPromptTransformed",
		"Stop", "ErrorOccurred", "Notification", "PermissionRequest", "PostToolUse", "PostToolUseFailure", "PreCompact", "PreToolUse", "SessionEnd", "SessionStart", "SubagentStop", "UserPromptSubmit"} {
		copilotHookEvents[e] = true
	}
}

var (
	credentialKey = regexp.MustCompile(`(?i)(?:api[_-]?key|token|secret|password|passwd|authorization|credential|private[_-]?key)`)
	envReference  = regexp.MustCompile(`^\$\{?[A-Za-z_][A-Za-z0-9_]*\}?$`)
)

// credentialValue finds literal values stored under credential-named keys (such as hook env or
// headers), which the text patterns miss once keys are JSON-quoted. Variable references are fine.
func credentialValue(v any) bool {
	switch value := v.(type) {
	case map[string]any:
		for k, child := range value {
			if text, ok := child.(string); ok && credentialKey.MatchString(k) && text != "" && !envReference.MatchString(text) {
				return true
			}
			if credentialValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if credentialValue(child) {
				return true
			}
		}
	}
	return false
}

// ContextHookEvent reports whether a Claude or Codex hook for event returns context that Copilot
// only accepts as {"additionalContext": ...}; those commands run through the output adapter.
func ContextHookEvent(source, event string) bool {
	return source != "copilot" && (event == "SessionStart" || event == "UserPromptSubmit")
}

// HookHandlers returns the command handlers inside an event's entries, in Claude/Codex nested
// ({matcher, hooks: [...]}) or flat Copilot/Codex form.
func HookHandlers(entries []any) []map[string]any {
	var out []map[string]any
	for _, e := range entries {
		entry := mapValue(e)
		if nested, ok := entry["hooks"].([]any); ok {
			for _, h := range nested {
				out = append(out, mapValue(h))
			}
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (s *scanner) hooks(source, p string, value any, native bool) {
	events := mapValue(value)
	if events == nil {
		s.info(source, "hooks", "Hooks configuration", "needs-review", "Hook schema is not a supported event map; original remains unchanged.")
		return
	}
	for _, event := range sortedKeys(events) {
		if native {
			s.info(source, "hooks", event, "native", "Existing hook in "+p+" may execute in the selected profile; review it before launch. No duplicate installed.")
			continue
		}
		i := Item{Source: source, Kind: "hooks", Name: event, Disposition: "needs-review", Detail: "Executable hook requires review before it can run in Copilot. Discovery does not run it."}
		entries, ok := events[event].([]any)
		if !ok {
			entries = []any{events[event]}
		}
		problem := ""
		if !copilotHookEvents[event] {
			problem = " Copilot has no " + event + " hook event."
		} else if runtime.GOOS == "windows" && source != "copilot" {
			// Claude and Codex commands do not establish a PowerShell contract.
			problem = " Claude and Codex hook commands need a PowerShell adaptation on Windows."
		} else if secretText.Match(encode(entries)) || credentialValue(entries) {
			problem = " The hook contains a credential-like value; move it to an environment variable first."
		}
		handlers := HookHandlers(entries)
		if len(handlers) == 0 && problem == "" {
			problem = " No hook commands were found."
		}
		for _, h := range handlers {
			if problem != "" {
				break
			}
			kind := str(h["type"])
			if h == nil || (kind != "" && kind != "command" && kind != "http" && kind != "prompt") {
				problem = " A hook entry uses a shape Copilot does not run; review it manually."
				continue
			}
			if kind != "" && kind != "command" {
				continue
			}
			platform := str(h["bash"])
			if runtime.GOOS == "windows" {
				platform = str(h["powershell"])
			}
			if platform == "" && str(h["command"]) == "" && str(h["exec"]) == "" {
				problem = " A hook command has no form that runs on this platform; review it manually."
			} else if ContextHookEvent(source, event) && str(h["command"]) == "" && str(h["bash"]) == "" {
				problem = " This hook's context output can be translated only for shell commands; review it manually."
			}
		}
		if problem != "" {
			i.Detail += problem
			s.add(i, candidate{})
			continue
		}
		c := candidate{}
		if _, _, e := s.source(p, &c); e != nil {
			s.add(i, candidate{})
			continue
		}
		a := Activation{Kind: "hook", Name: safeName(source + "-" + event), HookSource: source, HookEvent: event, Config: map[string]any{"entries": entries}}
		i.Selectable = true
		i.Disposition = "convertible"
		i.Detail = "Installs this hook in Copilot in its original format, so Copilot applies its matchers, timeouts, exit-code-2 blocking and permission decisions. Its commands run in future Copilot sessions."
		if ContextHookEvent(source, event) {
			i.Detail += " Context output is translated to Copilot's format by a reviewed adapter."
		}
		i.Preview = "Event: " + event + "\n" + redactedPreview(encode(entries))
		c.activation = &a
		s.add(i, c)
	}
}

// AdaptContextOutput maps a SessionStart or UserPromptSubmit hook's output to
// Copilot's shapes. Claude and Codex add plain stdout as context, or read
// hookSpecificOutput.additionalContext; a UserPromptSubmit "decision":"block" or
// "continue":false becomes Copilot's {"decision":"block"}. It rejects other
// behavior-bearing fields instead of silently losing permission decisions.
func AdaptContextOutput(source, event string, raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("{}\n"), nil
	}
	m, e := parseJSON(raw)
	if e != nil || m == nil {
		if source != "copilot" {
			return encode(map[string]any{"additionalContext": string(trimmed)}), nil
		}
		return nil, fmt.Errorf("hook output must be JSON: %v", e)
	}
	if source == "copilot" {
		for k := range m {
			if k != "additionalContext" {
				return nil, fmt.Errorf("unsupported hook output field %s", k)
			}
		}
		return encode(map[string]any{"additionalContext": str(m["additionalContext"])}), nil
	}
	allowed := map[string]bool{"hookSpecificOutput": true}
	if event == "UserPromptSubmit" {
		for _, k := range []string{"decision", "reason", "continue", "stopReason"} {
			allowed[k] = true
		}
	}
	for k := range m {
		if !allowed[k] {
			return nil, fmt.Errorf("unsupported hook output field %s", k)
		}
	}
	if d, ok := m["decision"]; ok && d != "block" {
		return nil, fmt.Errorf("unsupported hook decision %v", d)
	}
	if c, ok := m["continue"].(bool); m["decision"] == "block" || (ok && !c) {
		reason := str(m["reason"])
		if reason == "" {
			reason = str(m["stopReason"])
		}
		return PromptBlockOutput(reason), nil
	}
	if _, ok := m["hookSpecificOutput"]; !ok {
		return []byte("{}\n"), nil
	}
	out := mapValue(m["hookSpecificOutput"])
	if out == nil {
		return nil, errors.New("expected hookSpecificOutput object")
	}
	for k := range out {
		if k != "additionalContext" && k != "hookEventName" {
			return nil, fmt.Errorf("unsupported hook output field %s", k)
		}
	}
	if name := str(out["hookEventName"]); name != "" && name != event {
		return nil, errors.New("unexpected hook event")
	}
	return encode(map[string]any{"additionalContext": str(out["additionalContext"])}), nil
}

// PromptBlockOutput is the UserPromptSubmit output Copilot honours to stop a prompt.
// Copilot 1.0.105 shows, but does not block on, a prompt hook's exit code 2.
func PromptBlockOutput(reason string) []byte {
	return encode(map[string]any{"decision": "block", "reason": reason})
}

func (s *scanner) pluginEntries(source, p string, value any, native bool) {
	m := mapValue(value)
	if m == nil {
		return
	}
	for _, name := range sortedKeys(m) {
		if native {
			s.info(source, "plugin", name, "native", "Existing plugin activation is retained; policy and project overlays may affect effective state.")
			continue
		}
		// A plugin is usually in both the installed registry and enabledPlugins: list it once.
		// pluginDisabled reads both, so the first entry already reflects either one.
		if slices.ContainsFunc(s.report.Items, func(other Item) bool {
			return other.Kind == "plugin" && other.Source == source && other.Name == name
		}) {
			continue
		}
		i := Item{Source: source, Kind: "plugin", Name: name, Disposition: "needs-review", Detail: "A client-specific plugin registry is not portable. Reinstall only a known compatible plugin through Copilot."}
		disabled := s.pluginDisabled(source, name, m[name])
		spec := name
		if strings.Contains(spec, "@") && !strings.ContainsAny(spec, "/\\\r\n ") && !strings.Contains(spec, ":") {
			market := spec[strings.LastIndex(spec, "@")+1:]
			marketSource, problem := s.marketplaceSource(source, market)
			if problem != "" {
				i.Detail = problem
				s.add(i, candidate{})
				continue
			}
			c := candidate{}
			if _, _, e := s.source(p, &c); e == nil {
				i.Selectable = true
				i.Disposition = "convertible"
				i.Detail = "Installs this plugin with Copilot's plugin command, after adding the " + market + " marketplace that " + source + " recorded."
				if marketSource == "" {
					i.Detail = "Installs this plugin from Copilot's included " + market + " marketplace."
				}
				if disabled {
					i.Disposition = "needs-review"
					i.Detail = "This plugin is turned off in " + source + ", so it is not installed unless you select it. " + i.Detail
				}
				a := Activation{Kind: "plugin", Name: name, Specification: spec, MarketplaceSource: marketSource}
				c.activation = &a
				s.add(i, c)
				continue
			}
		}
		s.add(i, candidate{})
	}
}

// pluginDisabled reports a plugin the source app has turned off, in the entry itself or, for a
// plugin listed in the installed registry, in the app's own plugin settings.
func (s *scanner) pluginDisabled(source, name string, value any) bool {
	off := func(v any) bool {
		if on, ok := v.(bool); ok {
			return !on
		}
		on, ok := mapValue(v)["enabled"].(bool)
		return ok && !on
	}
	if off(value) {
		return true
	}
	switch source {
	case "claude":
		if m, e := readMap(filepath.Join(s.report.Options.ClaudeHome, "settings.json")); e == nil {
			return off(mapValue(m["enabledPlugins"])[name])
		}
	case "codex":
		var m map[string]any
		if b, _, e := readFile(filepath.Join(s.report.Options.CodexHome, "config.toml")); e == nil && toml.Unmarshal(b, &m) == nil {
			return off(mapValue(m["plugins"])[name])
		}
	}
	return false
}

// marketplaceSource finds where the source client got a plugin marketplace, as an argument for
// `copilot plugin marketplace add`. A non-empty problem means the plugin cannot be reinstalled as-is.
func (s *scanner) marketplaceSource(source, market string) (string, string) {
	if market == "awesome-copilot" {
		return "", ""
	}
	kind, value := "", ""
	switch source {
	case "claude":
		var src map[string]any
		if m, e := readMap(filepath.Join(s.report.Options.ClaudeHome, "plugins", "known_marketplaces.json")); e == nil {
			src = mapValue(mapValue(m[market])["source"])
		}
		if src == nil {
			if m, e := readMap(filepath.Join(s.report.Options.ClaudeHome, "settings.json")); e == nil {
				src = mapValue(mapValue(mapValue(m["extraKnownMarketplaces"])[market])["source"])
			}
		}
		switch kind = str(src["source"]); kind {
		case "github":
			value = str(src["repo"])
		case "git":
			value = str(src["url"])
		case "directory", "local":
			kind, value = "local", str(src["path"])
		}
	case "codex":
		b, _, e := readFile(filepath.Join(s.report.Options.CodexHome, "config.toml"))
		var m map[string]any
		if e == nil && toml.Unmarshal(b, &m) == nil {
			entry := mapValue(mapValue(m["marketplaces"])[market])
			kind, value = str(entry["source_type"]), str(entry["source"])
		}
	case "copilot":
		return "", "Plugins from another Copilot profile are reinstalled with that profile's marketplace in Copilot."
	}
	if value == "" || (kind != "github" && kind != "git" && kind != "local") {
		return "", "The " + market + " marketplace source is not recorded in " + source + "'s settings. Install this plugin manually in Copilot if a compatible marketplace exists."
	}
	if strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
		return "", "The recorded " + market + " marketplace source is not a plain location; review it manually."
	}
	if kind != "local" {
		if secretText.MatchString(value) || literalSecret([]byte(value)) {
			return "", "The recorded " + market + " marketplace address contains a credential; set up Git sign-in separately and add the marketplace manually."
		}
		if strings.Contains(value, "://") {
			u, e := url.Parse(value)
			hasPassword := false
			if e == nil {
				_, hasPassword = u.User.Password()
			}
			if e != nil || hasPassword || u.RawQuery != "" || ((u.Scheme == "http" || u.Scheme == "https") && u.User != nil) {
				return "", "The recorded " + market + " marketplace address contains credentials or parameters; set up Git sign-in separately and add the marketplace manually."
			}
		}
	}
	if kind == "local" {
		value = nativePath(value)
		if !filepath.IsAbs(value) || !exists(value) {
			return "", "The local " + market + " marketplace folder is missing; install this plugin manually in Copilot."
		}
		if under(filepath.Join(s.report.Options.CodexHome, ".tmp"), value) || strings.Contains(filepath.ToSlash(value), "/codex-runtimes/") {
			return "", "This plugin is bundled with the Codex app and works only inside Codex."
		}
	}
	return value, ""
}
func (s *scanner) plugins(source, root string, native bool) {
	p := filepath.Join(root, "plugins", "installed_plugins.json")
	if m, e := readMap(p); e == nil {
		if v, ok := m["plugins"]; ok {
			s.pluginEntries(source, p, v, native)
		}
	}
	for _, folder := range []string{"installed-plugins", "plugins/cache"} {
		path := filepath.Join(root, folder)
		if entries, e := os.ReadDir(path); e == nil && len(entries) > 0 {
			s.info(source, "plugin", "Plugin cache", "needs-review", "Found a plugin cache. Cache files and private plugin data are not copied; inspect named plugin specifications separately.")
		}
	}
}
func (s *scanner) automations(root string) {
	path := filepath.Join(root, "automations")
	entries, _ := os.ReadDir(path)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p := filepath.Join(path, entry.Name(), "automation.toml")
		b, _, e := readFile(p)
		if e != nil {
			continue
		}
		var m map[string]any
		if toml.Unmarshal(b, &m) != nil {
			continue
		}
		name := str(m["name"])
		if name == "" {
			name = entry.Name()
		}
		status := str(m["status"])
		s.info("codex", "automation", name, "unsupported", "Codex scheduler/thread integration; remains unchanged ("+status+"). It is not a Copilot hook or skill. Prompt content is not exported.")
	}
}
func (s *scanner) claudeMemory(root string) {
	path := filepath.Join(root, "projects")
	entries, _ := os.ReadDir(path)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p := filepath.Join(path, entry.Name(), "memory", "MEMORY.md")
		if exists(p) {
			s.memory("claude", p)
		}
	}
}
func (s *scanner) memory(source, p string) {
	c := candidate{}
	b, _, e := s.source(p, &c)
	if e != nil {
		return
	}
	name := filepath.Base(filepath.Dir(filepath.Dir(p))) + "/" + filepath.Base(p)
	i := Item{Source: source, Kind: "memory", Name: name, Disposition: "needs-review", Selectable: true, Detail: "Import selected reviewed facts as Markdown context. This does not populate Copilot's native memory service or copy conversation history.", Preview: redactedPreview(b)}
	if literalSecret(b) {
		i.Selectable = false
		i.Detail += " Credential-like text needs removing first" + secretWhere(b) + "."
	} else if importReference.Match(b) {
		i.Selectable = false
		i.Detail += " Additional file references need review before import."
	}
	dest := filepath.Join(s.report.Options.Destination, "imports", "memory", safeName(source+"-"+name)+".md")
	c.ops = []operation{{path: dest, content: b, mode: 0600}, {path: filepath.Join(s.report.Options.Destination, "copilot-instructions.md"), appendText: "\n\n<!-- Copilot Ready: reviewed memory facts -->\n@" + filepath.ToSlash(strings.TrimPrefix(dest, s.report.Options.Destination+string(filepath.Separator))) + "\n", mode: 0600}}
	s.add(i, c)
}

// conversations offers saved Claude Code and Codex conversations for import with
// `copilot sessions import`, which runs after setup without blocking it.
func (s *scanner) conversations() {
	o := s.report.Options
	record := sessions.OpenRecord(o.SessionRecord)
	if exists(filepath.Join(o.ClaudeHome, "projects")) {
		s.conversationItems("claude", fullConversations("claude"), "", sessions.ListClaude(o.ClaudeHome), record)
	}
	if o.Cursor && exists(filepath.Join(o.CursorHome, "projects")) {
		carries := "Your messages and Cursor's replies carry over, with each tool Cursor used named in its reply. Cursor doesn't record tool results, model reasoning or message times, so those aren't included, and each conversation is dated when Cursor last saved it."
		unmatched := "Cursor saves only a shortened form of the folder's path, and for these no single folder here matched it."
		s.conversationItems("cursor", carries, unmatched, sessions.ListCursor(o.CursorHome), record)
	}
	if !exists(o.CodexHome) {
		return
	}
	if o.CodexExecutable == "" {
		s.info("codex", "conversations", "Codex conversations", "unsupported", "Codex conversations are read through the Codex app, which was not found. Install or update Codex and scan again to bring them over.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	list, err := sessions.ListCodex(ctx, o.CodexExecutable, o.CodexHome)
	if err != nil {
		s.info("codex", "conversations", "Codex conversations", "unsupported", "Codex could not list its conversations: "+err.Error())
		return
	}
	s.conversationItems("codex", fullConversations("codex"), "", list, record)
}

func fullConversations(source string) string {
	return "Messages and tool calls carry over; model reasoning, images and per-message times stay in " + sessions.Product(source) + "."
}

// conversationItems offers an app's conversations. carries says what an imported conversation
// keeps; unmatched, if set, explains why some conversations have no folder here.
func (s *scanner) conversationItems(source, carries, unmatched string, list []sessions.Session, record *sessions.Record) {
	product := sessions.Product(source)
	var ready []sessions.Session
	imported, missing := 0, 0
	for _, c := range list {
		switch {
		case record.Has(s.report.Options.Destination, c.ExternalID()):
			imported++
		case !c.FolderExists():
			missing++
		default:
			ready = append(ready, c)
		}
	}
	plural := func(n int) string {
		if n == 1 {
			return "1 conversation"
		}
		return fmt.Sprintf("%d conversations", n)
	}
	if len(ready) > 0 {
		sort.Slice(ready, func(a, b int) bool { return ready[a].Updated.After(ready[b].Updated) })
		var preview strings.Builder
		for _, c := range ready {
			fmt.Fprintf(&preview, "%s · %s · %s\n", c.Updated.Format("2006-01-02"), c.Title, c.Cwd)
		}
		detail := "Brings your " + product + " conversations into Copilot so you can continue them with /resume. They import in the background after setup; Copilot opens without waiting. " + carries
		if imported > 0 {
			detail += " " + plural(imported) + " imported earlier will be skipped."
		}
		detail += " To list them in the Copilot desktop app too, turn that on when you review. The app shows only those from GitHub repositories."
		id := s.add(Item{Source: source, Kind: "conversations", Name: plural(len(ready)), Disposition: "convertible", Selectable: true, Detail: detail, Preview: redactedPreview([]byte(preview.String()))}, candidate{})
		s.report.conversations[id] = ready
	} else if imported > 0 {
		s.info(source, "conversations", plural(imported), "native", "Already imported into this Copilot profile.")
	}
	if missing > 0 {
		detail := "Copilot needs each conversation's folder on this computer. "
		if unmatched != "" {
			detail += unmatched + " "
		}
		s.info(source, "conversations", plural(missing)+" from missing folders", "unsupported", detail+"These stay in "+product+".")
	}
}

// nativePath turns a recorded folder into its plain form. The Codex app writes Windows paths in
// extended form (\\?\C:\... or \\?\UNC\server\share\...) and Claude may use forward slashes.
func nativePath(p string) string {
	if rest, ok := strings.CutPrefix(p, `\\?\UNC\`); ok {
		p = `\\` + rest
	} else {
		p = strings.TrimPrefix(p, `\\?\`)
	}
	return filepath.Clean(p)
}

// Conversations returns the source conversations a selected conversations item imports.
func Conversations(r Report, id string) []sessions.Session { return r.conversations[id] }

func (s *scanner) project(root string) {
	if e := noLinks(root); e != nil {
		s.warn(e.Error())
		return
	}
	for _, directory := range projectDirectories(root) {
		s.projectDirectory(directory)
	}
	if s.report.Options.Cursor {
		s.cursorNestedRules(root)
	}
}

func projectDirectories(root string) []string {
	if root == "" {
		return nil
	}
	// Locate the nearest Git/worktree boundary without scanning unrelated
	// parent contents. Without a repository marker, inspect only the selection.
	directories := []string{}
	foundRepository := false
	for cursor := root; ; cursor = filepath.Dir(cursor) {
		directories = append(directories, cursor)
		if exists(filepath.Join(cursor, ".git")) {
			foundRepository = true
			break
		}
		if filepath.Dir(cursor) == cursor {
			break
		}
	}
	if !foundRepository {
		directories = directories[:1]
	}
	return directories
}

func (s *scanner) projectDirectory(root string) {
	for _, p := range []string{"AGENTS.md", "CLAUDE.md", ".claude/CLAUDE.md", ".github/copilot-instructions.md"} {
		if exists(filepath.Join(root, p)) {
			s.info("project", "instructions", filepath.Base(p), "native", "Copilot discovers "+filepath.Join(root, p)+" when the selected folder is trusted. No repository files will be changed.")
		}
	}
	for _, p := range []string{".github/skills", ".agents/skills", ".claude/skills"} {
		s.skills("project", filepath.Join(root, p), true)
	}
	s.agents("project", filepath.Join(root, ".github", "agents"), true)
	s.walkFiles(filepath.Join(root, ".github", "hooks"), func(p string) {
		if strings.HasSuffix(p, ".json") {
			if m, e := readMap(p); e == nil {
				if h, ok := m["hooks"]; ok {
					s.hooks("project", p, h, true)
				}
			}
		}
	})
	for _, p := range []string{".github/instructions", ".claude/rules"} {
		if exists(filepath.Join(root, p)) {
			s.info("project", "rules", p, "native", "Project-scoped rules remain in place; effective applicability depends on paths and trust.")
		}
	}
	for _, p := range []string{".mcp.json", ".github/mcp.json"} {
		s.mcpFile("project", filepath.Join(root, p), true)
	}
	for _, p := range []string{".claude/settings.json", ".claude/settings.local.json", ".github/copilot/settings.json", ".github/copilot/settings.local.json"} {
		if m, e := readMap(filepath.Join(root, p)); e == nil {
			if h, ok := m["hooks"]; ok {
				s.hooks("project", filepath.Join(root, p), h, true)
			}
		}
	}
	if exists(filepath.Join(root, "MEMORY.md")) {
		s.info("project", "memory", "MEMORY.md", "needs-review", "A Markdown memory file is not an automatic native-memory import. Leave it in place and reference reviewed facts explicitly in project instructions if desired.")
	}
	s.prompts("claude-project", filepath.Join(root, ".claude/commands"), false)
	if s.report.Options.Cursor {
		s.cursorProject(root)
	}
	if exists(filepath.Join(root, ".codex")) {
		s.info("project", "settings", ".codex", "needs-review", "Project-local Codex settings are scoped to Codex. No repository configuration is rewritten automatically.")
	}
}

func BuildChanges(r Report, selectedIDs, overwriteIDs []string) ([]core.Change, error) {
	selected := map[string]bool{}
	for _, id := range selectedIDs {
		selected[id] = true
	}
	overwrite := map[string]bool{}
	for _, id := range overwriteIDs {
		overwrite[id] = true
	}
	items := map[string]Item{}
	for _, i := range r.Items {
		items[i.ID] = i
	}
	// Two copies of one skill would merge into one folder even where their files don't collide.
	skillFolders := map[string]string{}
	for _, id := range selectedIDs {
		item, ok := items[id]
		if !ok || item.Kind != "skill" || !selected[id] {
			continue
		}
		for _, op := range r.candidates[id].ops {
			rel, err := filepath.Rel(filepath.Join(r.Options.Destination, "skills"), op.path)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			folder := strings.ToLower(strings.SplitN(filepath.ToSlash(rel), "/", 2)[0])
			if owner, taken := skillFolders[folder]; taken && owner != id {
				return nil, fmt.Errorf("Both the %s and the %s copy of the skill %s are selected. Clear one of them.", items[owner].Source, item.Source, item.Name)
			}
			skillFolders[folder] = id
		}
	}
	contents := map[string][]byte{}
	modes := map[string]uint32{}
	owners := map[string]string{}
	pointerOwners := map[string]string{}
	order := []string{}
	for _, id := range selectedIDs {
		if !selected[id] {
			continue
		}
		delete(selected, id)
		item, ok := items[id]
		if !ok || !item.Selectable {
			return nil, errors.New("selected import is unknown or not compatible")
		}
		if item.Conflict && !overwrite[id] {
			return nil, fmt.Errorf("explicit conflict approval required for %s", item.Name)
		}
		c := r.candidates[id]
		if e := verifySources(c); e != nil {
			return nil, e
		}
		for _, op := range c.ops {
			snap, ok := r.snapshots[op.path]
			if !ok {
				return nil, errors.New("missing destination snapshot")
			}
			if e := verifySnapshot(op.path, snap.hash); e != nil {
				return nil, e
			}
			data, seen := contents[op.path]
			if !seen {
				data = append([]byte(nil), snap.content...)
				order = append(order, op.path)
			}
			for pointer := range op.pointers {
				key := strings.ToLower(filepath.Clean(op.path)) + pointer
				if owner, taken := pointerOwners[key]; taken && owner != id {
					return nil, fmt.Errorf("two selected imports set %s in %s; choose one", strings.TrimPrefix(pointer, "/mcpServers/"), filepath.Base(op.path))
				}
				pointerOwners[key] = id
			}
			if op.patch != nil || op.pointers != nil {
				var e error
				if op.patch != nil {
					data, e = patchJSON(data, op.patch)
				} else {
					data, e = patchPointers(data, op.pointers)
				}
				if e != nil {
					return nil, fmt.Errorf("cannot merge %s: %w", filepath.Base(op.path), e)
				}
			} else if op.appendText != "" {
				if !bytes.Contains(data, []byte(op.appendText)) {
					data = append(data, []byte(op.appendText)...)
				}
			} else {
				if seen && owners[op.path] != id && !bytes.Equal(data, op.content) {
					other := items[owners[op.path]]
					return nil, fmt.Errorf("The %s %s from %s and the one from %s write the same file (%s). Clear one of them.", item.Kind, item.Name, other.Source, item.Source, filepath.Base(op.path))
				}
				data = append([]byte(nil), op.content...)
			}
			contents[op.path] = data
			modes[op.path] = op.mode
			owners[op.path] = id
		}
	}
	var result []core.Change
	for _, p := range order {
		snap := r.snapshots[p]
		if bytes.Equal(snap.content, contents[p]) && snap.hash != "absent" {
			continue
		}
		mode := modes[p]
		if mode == 0 {
			mode = 0600
		}
		result = append(result, core.Change{Path: p, Content: contents[p], BeforeSHA256: snap.hash, Mode: mode, Source: "Reviewed customization import"})
	}
	return result, nil
}
func verifySources(c candidate) error {
	for p, hash := range c.sources {
		if e := verifySnapshot(p, hash); e != nil {
			return fmt.Errorf("source changed after review; scan again: %w", e)
		}
	}
	return nil
}
func verifySnapshot(p, hash string) error {
	b, _, e := readFile(p)
	if errors.Is(e, os.ErrNotExist) && hash == "absent" {
		return nil
	}
	if e != nil {
		return e
	}
	if digest(b) != hash {
		return fmt.Errorf("file changed after review: %s", p)
	}
	return nil
}
func patchJSON(b []byte, fields map[string]any) ([]byte, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}")
	}
	v, e := hujson.Parse(b)
	if e != nil {
		return nil, e
	}
	var ops []map[string]any
	for _, k := range sortedKeys(fields) {
		ops = append(ops, map[string]any{"op": "add", "path": "/" + jsonPointer(k), "value": fields[k]})
	}
	if e = v.Patch(encode(ops)); e != nil {
		return nil, e
	}
	v.Format()
	return v.Pack(), nil
}

// patchPointers sets values at JSON pointers such as /mcpServers/name, creating a missing parent
// object and keeping comments and unrelated entries.
func patchPointers(b []byte, values map[string]any) ([]byte, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}")
	}
	v, e := hujson.Parse(b)
	if e != nil {
		return nil, e
	}
	var ops []map[string]any
	created := map[string]bool{}
	for _, p := range sortedKeys(values) {
		if parent := p[:strings.LastIndex(p, "/")]; parent != "" && !created[parent] && v.Find(parent) == nil {
			ops = append(ops, map[string]any{"op": "add", "path": parent, "value": map[string]any{}})
			created[parent] = true
		}
		ops = append(ops, map[string]any{"op": "add", "path": p, "value": values[p]})
	}
	if e = v.Patch(encode(ops)); e != nil {
		return nil, e
	}
	v.Format()
	return v.Pack(), nil
}

func pointersExist(b []byte, values map[string]any) bool {
	v, e := hujson.Parse(b)
	if e != nil {
		return false
	}
	for p := range values {
		if v.Find(p) != nil {
			return true
		}
	}
	return false
}

func GetActivation(r Report, id string) (Activation, error) {
	c, ok := r.candidates[id]
	if !ok || c.activation == nil {
		return Activation{}, errors.New("no supported activation for this candidate")
	}
	if e := verifySources(c); e != nil {
		return Activation{}, e
	}
	return *c.activation, nil
}
