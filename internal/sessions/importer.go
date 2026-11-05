package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Importer runs `copilot sessions import` (added in Copilot CLI 1.0.85) for one profile.
type Importer struct {
	Copilot string   // verified runtime
	Env     []string // sets COPILOT_HOME to the target profile
	Profile string   // the same profile, where Copilot stores the imported session
	Dir     string   // scratch folder for transcripts
}

type importReply struct {
	OK          bool   `json:"ok"`
	Message     string `json:"message"`
	SessionID   string `json:"sessionId"`
	Diagnostics []struct {
		Line    int    `json:"line"`
		Message string `json:"message"`
	} `json:"diagnostics"`
}

// Import converts c and imports it, returning the new Copilot session id.
func (im Importer) Import(ctx context.Context, c Conversation) (string, error) {
	data, err := Interchange(c)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(im.Dir, "conversation-*.jsonl")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	// Setup's own calls skip auto-update so they run the runtime it verified, and keep their logs
	// out of the profile.
	out, runErr := runCopilot(ctx, im.Copilot, im.Profile, im.Dir, "", im.Env, []string{"sessions", "import", "--output", "json", f.Name()})
	var reply importReply
	start, end := bytes.IndexByte(out, '{'), bytes.LastIndexByte(out, '}')
	if start < 0 || end < start || json.Unmarshal(out[start:end+1], &reply) != nil {
		if runErr != nil {
			return "", fmt.Errorf("copilot sessions import failed: %v", runErr)
		}
		return "", errors.New("copilot sessions import gave no result")
	}
	if !reply.OK {
		msg := firstOf(reply.Message, "Copilot rejected the conversation")
		if len(reply.Diagnostics) > 0 {
			msg = fmt.Sprintf("%s Line %d: %s", msg, reply.Diagnostics[0].Line, reply.Diagnostics[0].Message)
		}
		return "", errors.New(msg)
	}
	// Copilot orders /resume and picks --continue by the session file's modification time, which
	// is the import time; give it the conversation's own last update instead.
	if !c.Updated.IsZero() && im.Profile != "" && filepath.Base(reply.SessionID) == reply.SessionID {
		_ = os.Chtimes(filepath.Join(im.Profile, "session-state", reply.SessionID, "events.jsonl"), c.Updated, c.Updated)
	}
	return reply.SessionID, nil
}
