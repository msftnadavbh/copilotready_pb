// Package sessions converts Claude Code, Codex and Cursor conversations into the semantic session JSONL
// that `copilot sessions import` reads, and runs that import with the verified runtime.
package sessions

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Session is one source conversation found during discovery.
type Session struct {
	Source  string // "claude", "codex" or "cursor"
	ID      string
	Title   string
	Cwd     string
	Branch  string
	Origin  string // Git remote the source recorded, if any
	Path    string // Claude or Cursor transcript file
	Created time.Time
	Updated time.Time
	// Loose is a conversation that wasn't started in a folder. It continues in the home folder,
	// and that folder's Git repository, if any, is not attached to it.
	Loose bool
}

func (s Session) ExternalID() string { return s.Source + ":" + s.ID }

// Product is the name of the app a conversation comes from.
func Product(source string) string {
	switch source {
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	}
	return "Claude Code"
}

// Event is one step of a conversation, in the order it happened.
type Event struct {
	Kind   string // "user", "assistant", "call" or "result"
	Text   string
	Model  string
	CallID string
	Name   string
	Args   any
	Failed bool
}

type Conversation struct {
	Session
	Events []Event
	// Note says what the source app didn't record. It opens the first message Copilot keeps.
	Note string
}

// Record remembers which conversations were imported into which profile. Copilot does not keep
// the source id, so importing the same conversation again would create a duplicate session.
type Record struct {
	path string
	mu   sync.Mutex
	data map[string]map[string]string
}

func OpenRecord(path string) *Record {
	r := &Record{path: path, data: map[string]map[string]string{}}
	r.load()
	return r
}

// load merges the record on disk, so setup windows running at the same time keep each other's
// entries and see each other's imports.
func (r *Record) load() {
	var disk map[string]map[string]string
	if b, err := os.ReadFile(r.path); err != nil || json.Unmarshal(b, &disk) != nil {
		return
	}
	for profile, ids := range disk {
		if r.data[profile] == nil {
			r.data[profile] = map[string]string{}
		}
		for id, copilotID := range ids {
			r.data[profile][id] = copilotID
		}
	}
}

func profileKey(profile string) string {
	profile = filepath.Clean(profile)
	if runtime.GOOS == "windows" {
		profile = strings.ToLower(profile)
	}
	return profile
}

// Path is the file the record is kept in.
func (r *Record) Path() string { return r.path }

func (r *Record) Has(profile, externalID string) bool {
	_, ok := r.Get(profile, externalID)
	return ok
}

// Get returns the Copilot session id recorded for a conversation.
func (r *Record) Get(profile, externalID string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.load()
	id, ok := r.data[profileKey(profile)][externalID]
	return id, ok
}

// All returns a copy of every entry, by profile.
func (r *Record) All() map[string]map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.load()
	all := map[string]map[string]string{}
	for profile, ids := range r.data {
		all[profile] = maps.Clone(ids)
	}
	return all
}

func (r *Record) Add(profile, externalID, copilotID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.load()
	key := profileKey(profile)
	if r.data[key] == nil {
		r.data[key] = map[string]string{}
	}
	r.data[key][externalID] = copilotID
	b, err := json.MarshalIndent(r.data, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), filepath.Base(r.path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), r.path)
}

// clip keeps text valid UTF-8 and at most max bytes, marking anything cut.
func clip(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n… [shortened by Copilot Ready]"
}

// titleLine is the first non-empty line of s on one line. Copilot rejects session names with
// double quotes, control characters, or more than 100 characters counted in UTF-16 units.
func titleLine(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case r != '\n' && unicode.IsControl(r):
			return ' '
		}
		return r
	}, s)
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		units := 0
		for i, r := range line {
			if units += utf16.RuneLen(r); units > 99 {
				return line[:i] + "…"
			}
		}
		return line
	}
	return ""
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return p != "" && err == nil && info.IsDir()
}

// FolderExists reports whether the conversation's folder is still on this computer; Copilot
// rejects an import whose working directory does not exist.
func (s Session) FolderExists() bool { return isDir(s.Cwd) }
