package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Claude Code's transcript format is internal and changes between releases, so only the fields
// below are read and every other entry type is skipped.
// https://code.claude.com/docs/en/sessions#where-transcripts-are-stored
var claudeFile = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\.jsonl$`)
var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// Command wrappers, hook output and interruption markers are not things the user wrote.
var claudeNoise = []string{"<command-name>", "<command-message>", "<command-args>", "<local-command-", "<bash-input>", "<bash-stdout>", "<bash-stderr>", "<task-notification>", "<user-prompt-submit-hook>", "[Request interrupted by user"}

type claudeEntry struct {
	Type        string  `json:"type"`
	UUID        string  `json:"uuid"`
	Parent      *string `json:"parentUuid"`
	Sidechain   bool    `json:"isSidechain"`
	Meta        bool    `json:"isMeta"`
	APIError    bool    `json:"isApiErrorMessage"`
	Cwd         string  `json:"cwd"`
	GitBranch   string  `json:"gitBranch"`
	Timestamp   string  `json:"timestamp"`
	CustomTitle string  `json:"customTitle"`
	AITitle     string  `json:"aiTitle"`
	Summary     string  `json:"summary"`
	Message     struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     any             `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// ListClaude finds transcripts under root/projects. It reads only the start of each file for the
// folder and first prompt, and its last 256 KiB for titles, which Claude Code re-appends there.
func ListClaude(root string) []Session {
	projects := filepath.Join(root, "projects")
	dirs, _ := os.ReadDir(projects)
	var out []Session
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(projects, d.Name()))
		for _, f := range files {
			if !f.IsDir() && claudeFile.MatchString(f.Name()) {
				if s, ok := claudeSession(filepath.Join(projects, d.Name(), f.Name())); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func claudeSession(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Session{}, false
	}
	s := Session{Source: "claude", ID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), Path: path, Updated: info.ModTime()}
	var custom, ai, summary, prompt string
	titles := func(e claudeEntry) {
		switch e.Type {
		case "custom-title":
			custom = firstOf(e.CustomTitle, custom)
		case "ai-title":
			ai = firstOf(e.AITitle, ai)
		case "summary":
			summary = firstOf(e.Summary, summary)
		}
	}
	r := bufio.NewReaderSize(f, 64<<10)
	var read int64
	for read < 8<<20 && (s.Cwd == "" || prompt == "") {
		line, err := r.ReadBytes('\n')
		read += int64(len(line))
		if e, ok := parseClaude(line); ok {
			if s.Cwd == "" && e.Cwd != "" {
				s.Cwd, s.Branch = e.Cwd, e.GitBranch
			}
			if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && s.Created.IsZero() {
				s.Created = t
			}
			if prompt == "" && e.Type == "user" && !e.Meta && !e.Sidechain {
				prompt = claudeUserText(e.Message.Content)
			}
			titles(e)
		}
		if err != nil {
			break
		}
	}
	if s.Cwd == "" || prompt == "" {
		return Session{}, false
	}
	if start := max(read, info.Size()-256<<10); start < info.Size() {
		// The head stopped on a line boundary. Further in, start one byte early and drop
		// through the first newline, which loses nothing if start was already a line start.
		ahead := start > read
		if ahead {
			start--
		}
		if _, err = f.Seek(start, io.SeekStart); err == nil {
			r = bufio.NewReaderSize(f, 64<<10)
			if ahead {
				_, _ = r.ReadBytes('\n')
			}
			for {
				line, err := r.ReadBytes('\n')
				if len(line) < 4096 {
					if e, ok := parseClaude(line); ok {
						titles(e)
					}
				}
				if err != nil {
					break
				}
			}
		}
	}
	s.Title = firstOf(custom, ai, summary, titleLine(prompt))
	return s, true
}

func parseClaude(line []byte) (claudeEntry, bool) {
	var e claudeEntry
	line = bytes.TrimSpace(bytes.TrimLeft(line, "\x00"))
	return e, len(line) > 0 && json.Unmarshal(line, &e) == nil
}

// LoadClaude rebuilds the conversation Claude Code would resume: the parentUuid chain from the
// last main-thread message back to the latest compaction, whose summary of the earlier conversation
// opens it. Parallel tool results can sit beside that chain, so results are matched to calls by
// tool_use_id instead.
func LoadClaude(s Session) (Conversation, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return Conversation{}, err
	}
	defer f.Close()
	var entries []claudeEntry
	byID := map[string]int{}
	results := map[string]claudeBlock{}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if e, ok := parseClaude(line); ok {
			if e.UUID != "" {
				byID[e.UUID] = len(entries)
			}
			if e.Type == "user" {
				for _, b := range claudeBlocks(e.Message.Content) {
					if _, seen := results[b.ToolUseID]; b.Type == "tool_result" && !seen {
						results[b.ToolUseID] = b
					}
				}
			}
			entries = append(entries, e)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return Conversation{}, err
		}
	}
	leaf := -1
	for i := len(entries) - 1; i >= 0 && leaf < 0; i-- {
		if (entries[i].Type == "user" || entries[i].Type == "assistant") && !entries[i].Sidechain {
			leaf = i
		}
	}
	var chain []claudeEntry
	seen := map[int]bool{}
	for i := leaf; i >= 0 && !seen[i]; {
		seen[i] = true
		e := entries[i]
		chain = append(chain, e)
		if e.Parent == nil {
			break // a compaction boundary or the first message
		}
		next, ok := byID[*e.Parent]
		if !ok {
			break
		}
		i = next
	}
	c := Conversation{Session: s}
	called := map[string]bool{}
	for i := len(chain) - 1; i >= 0; i-- {
		e := chain[i]
		switch e.Type {
		case "user":
			if !e.Meta {
				if text := claudeUserText(e.Message.Content); text != "" {
					c.Events = append(c.Events, Event{Kind: "user", Text: text})
				}
			}
		case "assistant":
			if e.APIError || e.Message.Model == "<synthetic>" {
				continue
			}
			for _, b := range claudeBlocks(e.Message.Content) {
				switch {
				case b.Type == "text":
					c.Events = append(c.Events, Event{Kind: "assistant", Text: b.Text, Model: e.Message.Model})
				case b.Type == "tool_use" && !called[b.ID]:
					called[b.ID] = true
					c.Events = append(c.Events, Event{Kind: "call", CallID: b.ID, Name: b.Name, Args: b.Input})
					if res, ok := results[b.ID]; ok {
						c.Events = append(c.Events, Event{Kind: "result", CallID: b.ID, Text: claudeResultText(res.Content), Failed: res.IsError})
					}
				}
			}
		}
	}
	return c, nil
}

func claudeBlocks(raw json.RawMessage) []claudeBlock {
	var blocks []claudeBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

// claudeUserText is what the user typed, without injected reminders or command wrappers.
func claudeUserText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var parts []string
		for _, b := range claudeBlocks(raw) {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "image":
				parts = append(parts, "[image not imported]")
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(systemReminder.ReplaceAllString(text, ""))
	for _, prefix := range claudeNoise {
		if strings.HasPrefix(text, prefix) {
			return ""
		}
	}
	return text
}

func claudeResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []string
	for _, b := range claudeBlocks(raw) {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			parts = append(parts, "[image not imported]")
		}
	}
	return strings.Join(parts, "\n")
}
