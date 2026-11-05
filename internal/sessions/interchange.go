package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	maxMessage = 64 << 10
	maxOutput  = 16 << 10
	maxArg     = 4 << 10
	// maxHistory keeps a resumed conversation near 60K tokens, half of a 128K window, so the first
	// prompt after /resume neither overflows nor waits on compaction. Older turns are left out.
	maxHistory = 240 << 10
)

// Model providers accept tool names made of letters, digits, "_" and "-", up to 64 characters.
var unsafeToolName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func toolName(name string) string {
	name = strings.Trim(unsafeToolName.ReplaceAllString(name, "_"), "_")
	if name == "" {
		name = "tool"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

var ErrEmpty = errors.New("no messages to import")

// Interchange renders c as Copilot's semantic session JSONL, version 1. Copilot 1.0.105 rejects a
// tool call without an assistant message earlier in its turn and expects each result before the
// next message, so tool-only turns get an empty assistant message and calls that never recorded a
// result are closed as failed. Only the newest turns that fit maxHistory are kept.
func Interchange(c Conversation) ([]byte, error) {
	created := c.Created.UTC()
	if created.IsZero() {
		created = time.Now().UTC()
	}
	stamp := created.Format(time.RFC3339Nano)
	application := map[string]string{"claude": "claude-code"}[c.Source]
	if application == "" {
		application = c.Source
	}
	header := map[string]any{"type": "session", "version": 1, "externalId": c.ExternalID(), "createdAt": stamp, "source": map[string]any{"application": application}}
	if c.Updated.After(created) {
		header["updatedAt"] = c.Updated.UTC().Format(time.RFC3339Nano)
	}
	if t := titleLine(c.Title); t != "" {
		header["title"] = t
	}
	// Copilot does not derive repository or branch from the folder, and rejects hosts other
	// than GitHub and Azure DevOps; only GitHub owner/name is certain, so others are left out.
	workspace := map[string]any{"workingDirectory": c.Cwd}
	origin, branch := c.Origin, c.Branch
	if (origin == "" || branch == "") && !c.Loose {
		gitOrigin, gitBranch := gitInfo(c.Cwd)
		origin, branch = firstOf(origin, gitOrigin), firstOf(branch, gitBranch)
	}
	if branch != "" {
		workspace["branch"] = branch
	}
	if owner, name, ok := githubRepository(origin); ok {
		workspace["repository"] = map[string]any{"host": "github.com", "owner": owner, "name": name}
	}
	header["workspace"] = workspace

	n, turn, assistantInTurn := 0, 0, false
	open, order := map[string]string{}, []string{}
	var turns [][]map[string]any
	emit := func(rec map[string]any) string {
		n++
		rec["id"], rec["timestamp"], rec["turnId"] = fmt.Sprintf("r%d", n), stamp, fmt.Sprintf("t%d", turn)
		turns[len(turns)-1] = append(turns[len(turns)-1], rec)
		return rec["id"].(string)
	}
	closeCalls := func() {
		for _, source := range order {
			if id, ok := open[source]; ok {
				emit(map[string]any{"type": "tool-result", "toolCallId": id, "success": false, "error": map[string]any{"message": "No result was recorded."}})
			}
		}
		open, order = map[string]string{}, nil
	}
	message := func(role, text, model string) {
		closeCalls()
		rec := map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": "text", "text": text}}}
		if model != "" {
			rec["model"] = model
		}
		emit(rec)
	}
	for _, e := range mergeAssistant(c.Events) {
		text := strings.TrimSpace(e.Text)
		switch e.Kind {
		case "user":
			if text == "" {
				continue
			}
			closeCalls()
			turn++
			turns = append(turns, nil)
			assistantInTurn = false
			message("user", clip(text, maxMessage), "")
		case "assistant":
			if turn > 0 && text != "" {
				message("assistant", clip(text, maxMessage), e.Model)
				assistantInTurn = true
			}
		case "call":
			if _, seen := open[e.CallID]; turn == 0 || seen {
				continue
			}
			if !assistantInTurn {
				message("assistant", "", "")
				assistantInTurn = true
			}
			rec := map[string]any{"type": "tool-call", "name": toolName(e.Name)}
			if args := toolArguments(e.Args); args != nil {
				rec["arguments"] = args
			}
			open[e.CallID] = emit(rec)
			order = append(order, e.CallID)
		case "result":
			id, ok := open[e.CallID]
			if !ok {
				continue
			}
			delete(open, e.CallID)
			rec := map[string]any{"type": "tool-result", "toolCallId": id, "success": !e.Failed}
			if text != "" {
				rec["content"] = clip(text, maxOutput)
			}
			if e.Failed {
				rec["error"] = map[string]any{"message": clip(firstOf(text, "The tool reported a failure."), 2000)}
			}
			emit(rec)
		}
	}
	closeCalls()
	if turn == 0 {
		return nil, ErrEmpty
	}
	turns[len(turns)-1] = fitTurn(turns[len(turns)-1], maxHistory)
	encoded := make([][]byte, len(turns))
	first, size := len(turns), 0
	for first > 0 {
		var b bytes.Buffer
		for _, rec := range turns[first-1] {
			line, _ := json.Marshal(rec)
			b.Write(append(line, '\n'))
		}
		// The newest turn is always kept; its messages are already clipped.
		if first < len(turns) && size+b.Len() > maxHistory {
			break
		}
		first--
		encoded[first], size = b.Bytes(), size+b.Len()
	}
	if first > 0 || c.Note != "" {
		opening := turns[first][0]["content"].([]any)[0].(map[string]any)
		note := "Imported from " + Product(c.Source) + "."
		if first > 0 {
			note += fmt.Sprintf(" The %d earlier turns are left out so the conversation fits Copilot's context.", first)
		}
		if c.Note != "" {
			note += " " + c.Note
		}
		opening["text"] = "[" + note + "]\n\n" + opening["text"].(string)
		line, _ := json.Marshal(turns[first][0])
		rest := encoded[first][bytes.IndexByte(encoded[first], '\n')+1:]
		encoded[first] = append(append(line, '\n'), rest...)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(header); err != nil {
		return nil, err
	}
	for _, b := range encoded[first:] {
		buf.Write(b)
	}
	return buf.Bytes(), nil
}

// fitTurn keeps a turn's request and its newest steps within budget. A tool call is kept or left
// out together with its result, and an empty assistant message keeps a first kept call valid.
func fitTurn(records []map[string]any, budget int) []map[string]any {
	size := func(rec map[string]any) int { b, _ := json.Marshal(rec); return len(b) + 1 }
	total := 0
	for _, rec := range records {
		total += size(rec)
	}
	if total <= budget || len(records) < 2 {
		return records
	}
	var steps [][]int
	stepOfCall := map[string]int{}
	for i := 1; i < len(records); i++ {
		if id, ok := records[i]["toolCallId"].(string); ok {
			if step, found := stepOfCall[id]; found {
				steps[step] = append(steps[step], i)
				continue
			}
		}
		if records[i]["type"] == "tool-call" {
			stepOfCall[records[i]["id"].(string)] = len(steps)
		}
		steps = append(steps, []int{i})
	}
	keep, left, kept := map[int]bool{}, budget-size(records[0])-256, 0
	for step := len(steps) - 1; step >= 0; step-- {
		need := 0
		for _, i := range steps[step] {
			need += size(records[i])
		}
		if need > left && kept > 0 {
			break
		}
		left -= need
		kept++
		for _, i := range steps[step] {
			keep[i] = true
		}
	}
	request := records[0]["content"].([]any)[0].(map[string]any)
	request["text"] = fmt.Sprintf("[%d earlier steps of this request are left out so it fits Copilot's context.]\n\n%s", len(steps)-kept, request["text"])
	out, assistant := []map[string]any{records[0]}, false
	for i := 1; i < len(records); i++ {
		if !keep[i] {
			continue
		}
		if records[i]["type"] == "tool-call" && !assistant {
			out = append(out, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": ""}}, "id": records[i]["id"].(string) + "a", "timestamp": records[i]["timestamp"], "turnId": records[i]["turnId"]})
		}
		assistant = assistant || records[i]["type"] == "message" || records[i]["type"] == "tool-call"
		out = append(out, records[i])
	}
	return out
}

// mergeAssistant joins consecutive assistant texts, which sources often store as separate parts.
func mergeAssistant(events []Event) []Event {
	out := make([]Event, 0, len(events))
	for _, e := range events {
		if last := len(out) - 1; e.Kind == "assistant" && last >= 0 && out[last].Kind == "assistant" {
			out[last].Text = strings.TrimSpace(out[last].Text + "\n\n" + e.Text)
			out[last].Model = firstOf(out[last].Model, e.Model)
			continue
		}
		out = append(out, e)
	}
	return out
}

func toolArguments(v any) any {
	if v == nil {
		return nil
	}
	if _, ok := v.(map[string]any); !ok {
		v = map[string]any{"input": v}
	}
	return shrink(v)
}

// shrink shortens long strings inside tool arguments, such as whole files passed to an edit tool.
func shrink(v any) any {
	switch x := v.(type) {
	case string:
		return clip(x, maxArg)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, y := range x {
			m[k] = shrink(y)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, y := range x {
			a[i] = shrink(y)
		}
		return a
	}
	return v
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
