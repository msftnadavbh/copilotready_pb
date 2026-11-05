package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"copilotready/internal/sessions"
)

// The Copilot app's sidebar lists a CLI session only under a project the user added, while
// Settings → Sessions → Show Copilot CLI Session is on (it starts Off), and only from a Git
// repository. Its documented link ghapp://sessions/<id> opens a session regardless: after the user
// confirms "Open session?", the app adds the repository or folder and keeps the session in the
// sidebar. Copilot Ready sends that link for conversations already in Copilot's session index;
// the confirmation stays with the user.

// openBatch caps how many confirmations one click asks for. Choosing the action again continues.
const openBatch = 20

// openURL hands a URL to the system's default handler. Tests replace it.
var openURL = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// ghappRegistered reports whether Windows lists a handler for ghapp links. rundll32 reports success
// even when nothing handles a link, so without one the result says what to check. An app installed
// from a package can register its links without these keys, so the links are still sent. Tests
// replace it.
var ghappRegistered = func() bool {
	for _, key := range []string{`HKCR\ghapp`, `HKCU\Software\Classes\ghapp`} {
		if exec.Command("reg", "query", key).Run() == nil {
			return true
		}
	}
	return false
}

const noConversationsToOpen = "There are no imported conversations to send to the Copilot app."

// openPause spaces the links so the app queues them in order.
var openPause = 300 * time.Millisecond

// sessionID is the shape of a Copilot session id; anything else in a record is not sent as a link.
var sessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// openInApp sends the next batch of conversations, newest first, to the Copilot app. A
// conversation counts once it is in Copilot's session index: added by setup's turn, or continued by
// the user in the CLI since its import. Once every one has been sent, again starts over, so one the
// user dismissed in the app can be sent again; without it, that is only reported.
func (a *app) openInApp(again bool) (string, error) {
	// Two pages choosing this at once would send the same links twice.
	a.openMu.Lock()
	defer a.openMu.Unlock()
	profile := a.existingProfile()
	opened := sessions.OpenRecord(filepath.Join(a.root, "opened-in-app.json"))
	imported := sessions.OpenRecord(filepath.Join(a.root, "imported-sessions.json")).All()
	store := filepath.Join(profile, "session-store.db")
	indexed, err := sessions.IndexedSessions(store)
	// Conversations setup added to the app don't need the list; only ones continued since do.
	unreadable := ""
	if _, statErr := os.Stat(store); err != nil && !errors.Is(statErr, os.ErrNotExist) && len(imported) > 0 {
		unreadable = fmt.Sprintf("Couldn't read Copilot's session list (%v), so conversations you continued in Copilot can't be found. Quit any Copilot that's running and choose Open in the Copilot app again.", err)
	}
	shown := a.shownRecord().All()
	candidates := map[string]map[string]bool{}
	for p, ids := range shown {
		for id := range ids {
			if candidates[p] == nil {
				candidates[p] = map[string]bool{}
			}
			candidates[p][id] = true
		}
	}
	for p, ids := range imported {
		for _, id := range ids {
			if samePath(p, profile) && indexed[id] {
				if candidates[p] == nil {
					candidates[p] = map[string]bool{}
				}
				candidates[p][id] = true
			}
		}
	}
	type conversation struct {
		id   string
		when time.Time
	}
	var todo, sentBefore []conversation
	elsewhere := 0
	for p, ids := range candidates {
		if !samePath(p, profile) {
			elsewhere += len(ids)
			continue
		}
		for id := range ids {
			if !sessionID.MatchString(id) {
				continue
			}
			info, err := os.Stat(filepath.Join(profile, "session-state", id, "events.jsonl"))
			if err != nil {
				continue // deleted in Copilot since
			}
			if opened.Has(profile, id) {
				sentBefore = append(sentBefore, conversation{id, info.ModTime()})
				continue
			}
			todo = append(todo, conversation{id, info.ModTime()})
		}
	}
	prefix := ""
	if unreadable != "" {
		prefix = unreadable + " "
	}
	if len(todo) == 0 {
		if len(sentBefore) == 0 {
			if unreadable != "" {
				return "", errors.New(unreadable)
			}
			if elsewhere > 0 {
				return "", errors.New("Your conversations were imported into a separate Copilot profile, which the Copilot app doesn't read. Run setup with your existing profile to use them in the app.")
			}
			return noConversationsToOpen, nil
		}
		if !again {
			return "Every imported conversation has already been sent to the Copilot app. To send them all again, for any you dismissed there, choose Open in the Copilot app.", nil
		}
		// Every one was sent, but the user may have dismissed some at Open session?. Start over:
		// the app opens the ones already allowed without asking again.
		if err := os.Remove(opened.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("Couldn't start sending the conversations again (%v).", err)
		}
		opened = sessions.OpenRecord(opened.Path())
		todo = sentBefore
		prefix += "Every conversation had already been sent, so they're sent again for any you dismissed; the app opens the ones you already allowed without asking. "
	}
	slices.SortFunc(todo, func(x, y conversation) int { return y.when.Compare(x.when) })
	plural := func(n int) string {
		return fmt.Sprintf("%d conversation%s", n, map[bool]string{true: "", false: "s"}[n == 1])
	}
	sent := 0
	for _, c := range todo[:min(len(todo), openBatch)] {
		if sent > 0 {
			time.Sleep(openPause)
		}
		if err := openURL("ghapp://sessions/" + c.id); err != nil {
			if sent == 0 {
				return "", errors.New("The GitHub Copilot app didn't open. Install it, sign in, and choose this again. " + manualAppSteps)
			}
			return "", fmt.Errorf("Sent %s to the Copilot app, then it stopped opening; %d remain. Choose Allow for the ones it shows, then choose Open in the Copilot app again for the rest.", plural(sent), len(todo)-sent)
		}
		// Without the record, the next click would send the same conversations again.
		if err := opened.Add(profile, c.id, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return "", fmt.Errorf("Sent %s to the Copilot app, but couldn't record which ones (%v). Make the setup folder writable before choosing Open in the Copilot app again, or the same conversations are sent again.", plural(sent+1), err)
		}
		sent++
	}
	msg := prefix + fmt.Sprintf("Sent %s to the Copilot app, newest first. In the app, choose Allow for each one; it then stays in the sidebar under its repository or folder.", plural(sent))
	if left := len(todo) - sent; left > 0 {
		msg += fmt.Sprintf(" %d more remain; choose Open in the Copilot app again to send the next ones.", left)
	}
	if runtime.GOOS == "windows" && !ghappRegistered() {
		msg += " Windows doesn't list the Copilot app for these links, so if nothing opened, install the app, sign in and choose this again. " + manualAppSteps
	}
	return msg, nil
}

func samePath(x, y string) bool {
	x, y = filepath.Clean(x), filepath.Clean(y)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		// Both systems usually use case-insensitive volumes.
		return strings.EqualFold(x, y)
	}
	return x == y
}
