package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// Cursor writes each agent conversation, from the app and from the Cursor CLI, as JSONL under
// ~/.cursor/projects/<folder>/agent-transcripts, where <folder> is the conversation's folder with
// separators turned into hyphens. The format is not documented: each line has a role and the
// message's text and tool_use blocks, without tool results, ids or times. Codex's Cursor import
// reads the same files (openai/codex, codex-rs/external-agent-migration).

const cursorNote = "Cursor doesn't record tool results or message times, so the tools it used are listed without their output."

type cursorEntry struct {
	Role      string          `json:"role"`
	Cwd       string          `json:"cwd"`
	Time      string          `json:"timestamp"`
	Timestamp int64           `json:"timestamp_ms"`
	Meta      bool            `json:"isMeta"`
	Sidechain bool            `json:"isSidechain"`
	Message   json.RawMessage `json:"message"`
}

type cursorBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	Input any    `json:"input"`
}

// ListCursor finds Cursor transcripts under root/projects. A subagent's transcript is part of
// its parent conversation and is skipped. Chats not started in a folder are kept under the name
// empty-window; like Codex's import, they continue in the folder that holds root, the home folder.
func ListCursor(root string) []Session {
	projects := filepath.Join(root, "projects")
	dirs, _ := os.ReadDir(projects)
	byID := map[string]Session{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		// The folder is looked up once per project, also when it isn't found.
		folder, resolved := "", false
		loose := d.Name() == "empty-window"
		transcripts := filepath.Join(projects, d.Name(), "agent-transcripts")
		_ = filepath.WalkDir(transcripts, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if e.IsDir() && e.Name() == "subagents" {
				return filepath.SkipDir
			}
			if e.IsDir() || e.Type()&fs.ModeSymlink != 0 || !strings.HasSuffix(e.Name(), ".jsonl") {
				return nil
			}
			s, ok := cursorSession(p)
			if !ok {
				return nil
			}
			// A chat outside any folder stays loose even when Cursor recorded the home folder.
			s.Loose = loose
			if s.Cwd == "" {
				if !resolved && loose {
					folder = filepath.Dir(root)
				} else if !resolved {
					folder = CursorFolder(d.Name(), cursorRoots(d.Name()))
				}
				s.Cwd, resolved = folder, true
			}
			if prior, seen := byID[s.ID]; !seen || s.Updated.After(prior.Updated) {
				byID[s.ID] = s
			}
			return nil
		})
	}
	out := make([]Session, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	return out
}

func cursorSession(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Session{}, false
	}
	s := Session{Source: "cursor", ID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), Path: path, Updated: info.ModTime()}
	r := bufio.NewReaderSize(f, 64<<10)
	// Reading stops once the first prompt and a folder are found. A transcript that records no
	// folder is read for at most 1 MB past its first prompt.
	var read, limit int64 = 0, 8 << 20
	prompt := ""
	for read < limit && (prompt == "" || s.Cwd == "") {
		line, err := r.ReadBytes('\n')
		read += int64(len(line))
		if e, ok := parseCursor(line); ok {
			s.Cwd = firstOf(s.Cwd, e.Cwd)
			if t := e.time(); !t.IsZero() && s.Created.IsZero() {
				s.Created = t
			}
			if e.Role == "user" && prompt == "" {
				if prompt = cursorUserText(cursorBlocks(e.Message)); prompt != "" {
					limit = min(limit, read+1<<20)
				}
			}
		}
		if err != nil {
			break
		}
	}
	if prompt == "" {
		return Session{}, false
	}
	if s.Created.IsZero() {
		s.Created = s.Updated
	}
	s.Title = titleLine(prompt)
	return s, true
}

func parseCursor(line []byte) (cursorEntry, bool) {
	var e cursorEntry
	line = bytes.TrimSpace(line)
	return e, len(line) > 0 && json.Unmarshal(line, &e) == nil && !e.Meta && !e.Sidechain
}

func (e cursorEntry) time() time.Time {
	if t, err := time.Parse(time.RFC3339Nano, e.Time); err == nil {
		return t
	}
	if e.Timestamp > 0 {
		return time.UnixMilli(e.Timestamp)
	}
	return time.Time{}
}

// cursorBlocks reads a message's content, written either as a string or as a list of blocks.
func cursorBlocks(message json.RawMessage) []cursorBlock {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &m) != nil {
		return nil
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []cursorBlock{{Type: "text", Text: text}}
	}
	var blocks []cursorBlock
	_ = json.Unmarshal(m.Content, &blocks)
	return blocks
}

// cursorUserText is what the user typed, without the time, command list and <user_query> tags
// Cursor wraps around it.
func cursorUserText(blocks []cursorBlock) string {
	var parts []string
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if text := cursorUnwrap(b.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// cursorUnwrap removes Cursor's envelope only where Cursor puts it: <timestamp> and
// <cursor_commands> elements at the start or end of the message, and one <user_query> element
// around the rest. The same tags typed inside a request are kept.
func cursorUnwrap(text string) string {
	t := strings.TrimSpace(text)
	for changed := true; changed; {
		changed = false
		for _, tag := range []string{"timestamp", "cursor_commands"} {
			open, close := "<"+tag+">", "</"+tag+">"
			if strings.HasPrefix(t, open) {
				if i := strings.Index(t, close); i >= 0 {
					t, changed = strings.TrimSpace(t[i+len(close):]), true
				}
			}
			if strings.HasSuffix(t, close) {
				if i := strings.LastIndex(t, open); i >= 0 {
					t, changed = strings.TrimSpace(t[:i]), true
				}
			}
		}
	}
	const open, close = "<user_query>", "</user_query>"
	if len(t) >= len(open)+len(close) && strings.HasPrefix(t, open) && strings.HasSuffix(t, close) {
		t = strings.TrimSpace(t[len(open) : len(t)-len(close)])
	}
	return t
}

// LoadCursor reads a Cursor transcript. Tool calls have no recorded result, so they become a line
// in Cursor's reply naming the tool and what it was given, rather than calls that look failed.
func LoadCursor(s Session) (Conversation, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return Conversation{}, err
	}
	defer f.Close()
	c := Conversation{Session: s, Note: cursorNote}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if e, ok := parseCursor(line); ok {
			blocks := cursorBlocks(e.Message)
			switch e.Role {
			case "user":
				if text := cursorUserText(blocks); text != "" {
					c.Events = append(c.Events, Event{Kind: "user", Text: text})
				}
			case "assistant":
				var parts []string
				for _, b := range blocks {
					switch b.Type {
					case "text":
						if t := strings.TrimSpace(b.Text); t != "" {
							parts = append(parts, t)
						}
					case "tool_use":
						parts = append(parts, cursorToolLine(b))
					}
				}
				if len(parts) > 0 && len(c.Events) > 0 {
					c.Events = append(c.Events, Event{Kind: "assistant", Text: strings.Join(parts, "\n\n")})
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return Conversation{}, err
		}
	}
	if len(c.Events) == 0 {
		return Conversation{}, ErrEmpty
	}
	return c, nil
}

// cursorToolLine names a tool and the one argument that says what it did, such as a command or
// a file. The whole input, which can be a file's contents, is left out.
func cursorToolLine(b cursorBlock) string {
	line := "_Used " + firstOf(b.Name, "a tool")
	if input, ok := b.Input.(map[string]any); ok {
		for _, key := range []string{"command", "path", "file_path", "target_file", "pattern", "query", "url"} {
			if v, ok := input[key].(string); ok && strings.TrimSpace(v) != "" {
				v = strings.Join(strings.Fields(v), " ")
				if len([]rune(v)) > 200 {
					v = string([]rune(v)[:200]) + "…"
				}
				line += ": `" + strings.ReplaceAll(v, "`", "'") + "`"
				break
			}
		}
	}
	return line + "_"
}

// cursorRoots is where a Cursor folder name starts: a Windows drive for names like c-Users-…,
// otherwise the filesystem root.
func cursorRoots(name string) []string {
	if runtime.GOOS == "windows" {
		if len(name) > 2 && name[1] == '-' && unicode.IsLetter(rune(name[0])) {
			return []string{strings.ToUpper(name[:1]) + `:\`}
		}
		return nil
	}
	return []string{string(filepath.Separator)}
}

// CursorFolder finds the folder a Cursor project name stands for. The name is the folder's path
// with the characters between letters and digits turned into hyphens, so it can't be decoded;
// instead each level's real folder names are encoded the same way and matched against it. When
// two folders match, such as my-app/web and my/app/web, neither is chosen, so a conversation is
// never continued in the wrong folder. The search reads at most 256 folders and returns "" when
// it finds no single match.
func CursorFolder(name string, roots []string) string {
	budget := 256
	var found []string
	var search func(dir, rest string)
	search = func(dir, rest string) {
		if budget <= 0 || len(found) > 1 {
			return
		}
		budget--
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			encoded := cursorEncode(e.Name())
			if encoded == "" || !strings.HasPrefix(strings.ToLower(rest), strings.ToLower(encoded)) {
				continue
			}
			next, path := rest[len(encoded):], filepath.Join(dir, e.Name())
			if next != "" && !strings.HasPrefix(next, "-") || !isDir(path) {
				continue
			}
			if next == "" {
				found = append(found, path)
			} else {
				search(path, next[1:])
			}
		}
	}
	for _, root := range roots {
		rest := cursorEncode(name)
		if volume := filepath.VolumeName(root); volume != "" && filepath.Clean(root) == volume+string(filepath.Separator) {
			prefix := cursorEncode(volume) + "-"
			if len(rest) < len(prefix) || !strings.EqualFold(rest[:len(prefix)], prefix) {
				continue
			}
			rest = rest[len(prefix):]
		}
		search(root, rest)
	}
	if len(found) != 1 || budget <= 0 {
		return ""
	}
	return found[0]
}

// cursorEncode turns each run of characters other than ASCII letters and digits into one hyphen
// and drops hyphens at either end. Applied to both sides, it matches whether Cursor writes one
// hyphen per character or one per run.
func cursorEncode(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
