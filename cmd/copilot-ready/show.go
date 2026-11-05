package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"copilotready/internal/sessions"
)

// The Copilot app lists CLI sessions from Copilot's session index. `copilot sessions import`
// does not add sessions there, so setup runs one offline turn per imported conversation against
// a local stand-in model (see sessions.Indexer). Nothing leaves the computer and no AI credits
// are used, but each conversation ends with that one short exchange.

type showJob struct{ id, title, application string }

func (a *app) shownRecord() *sessions.Record {
	return sessions.OpenRecord(filepath.Join(a.root, "indexed-sessions.json"))
}

func (a *app) setPhase(phase string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.imports.Phase = phase
}

// showConversations adds imported conversations to Copilot's session index. Conversations it
// added before, or that the user has continued since, already are and are skipped.
func (a *app) showConversations(ctx context.Context, copilot, profile string, jobs []showJob) {
	a.setPhase("show")
	shown := a.shownRecord()
	index := filepath.Join(profile, "session-store.db")
	// No index yet means nothing is in it. An index that can't be read must not look empty:
	// conversations the user already continued would get a second exchange and run hooks again.
	indexed, indexErr := sessions.IndexedSessions(index)
	if errors.Is(indexErr, fs.ErrNotExist) {
		indexErr = nil
	}
	var todo []showJob
	for _, j := range jobs {
		if shown.Has(profile, j.id) || indexed[j.id] {
			continue
		}
		todo = append(todo, j)
	}
	a.mu.Lock()
	p := a.imports
	p.ShowTotal += len(jobs)
	p.ShowDone += len(jobs) - len(todo)
	p.Shown += len(jobs) - len(todo)
	a.mu.Unlock()
	report := func(failed bool, text string, j showJob, err error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if failed {
			p.ShowFailed++
		}
		if len(p.Failures) < 50 {
			p.Failures = append(p.Failures, fmt.Sprintf("%s: %s (%s): %v", text, j.title, j.application, err))
		}
	}
	fail := func(j showJob, err error) { report(true, "Not added to the Copilot app", j, err) }
	if len(todo) == 0 {
		return
	}
	err := indexErr
	var dir string
	if err == nil {
		dir, err = os.MkdirTemp(a.root, "show-")
	}
	if err == nil {
		defer os.RemoveAll(dir)
	}
	ix := &sessions.Indexer{Copilot: copilot, Env: a.manager.Environment(profile), Profile: profile, Dir: dir}
	if err == nil {
		err = ix.Start()
	}
	if err != nil {
		for _, j := range todo {
			fail(j, fmt.Errorf("nothing was run: %w", err))
		}
		a.mu.Lock()
		p.ShowDone += len(todo)
		a.mu.Unlock()
		return
	}
	defer ix.Close()
	results := make([]error, len(todo))
	next := make(chan int)
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range next {
				results[i] = ix.Index(ctx, todo[i].id, todo[i].application)
				a.mu.Lock()
				p.ShowDone++
				a.mu.Unlock()
			}
		}()
	}
	for i := range todo {
		next <- i
	}
	close(next)
	workers.Wait()
	// Copilot's own index is the proof: a conversation in it is listed by the app and is never
	// given a second exchange, even if its turn reported an error.
	indexed, err = sessions.IndexedSessions(index)
	for i, j := range todo {
		ws, _ := sessions.ReadWorkspace(profile, j.id)
		switch {
		case err != nil:
			fail(j, fmt.Errorf("Copilot's session list could not be checked: %w", err))
		case !indexed[j.id]:
			fail(j, firstErr(results[i], errors.New("Copilot did not add it to its session list")))
		default:
			_ = shown.Add(profile, j.id, time.Now().UTC().Format(time.RFC3339))
			a.mu.Lock()
			p.Shown++
			a.mu.Unlock()
			if results[i] != nil {
				report(false, "Added to the Copilot app with a problem", j, results[i])
			} else if ws.Branch != "" && ws.GitRoot == "" {
				report(false, "Added to the Copilot app without its repository", j, errors.New("Copilot did not record its Git folder"))
			}
		}
	}
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// finishLater installs the hooks and plugins held back while conversations were added to the
// Copilot app. The hooks are a transaction of their own, which Undo restores with the setup.
func (a *app) finishLater(ctx context.Context, copilot, profile string, later afterShowing) {
	if len(later.hooks) == 0 && len(later.plugins) == 0 {
		return
	}
	a.setPhase("finish")
	var checks []map[string]string
	if len(later.hooks) > 0 {
		check := map[string]string{"name": "Hooks", "status": "passed", "detail": "Installed after your conversations were added to the Copilot app."}
		if _, err := a.manager.Apply(later.hooks, later.roots); err != nil {
			check["status"], check["detail"] = "needs attention", "Not installed: "+err.Error()
		}
		checks = append(checks, check)
	}
	checks = append(checks, a.installPlugins(ctx, later.report, copilot, profile, later.project, later.plugins)...)
	a.mu.Lock()
	defer a.mu.Unlock()
	if result, ok := a.result.(map[string]any); ok {
		validation, _ := result["validation"].([]map[string]string)
		result["validation"] = append(validation, checks...)
	}
}

// showImported is the maintenance action for conversations imported by an earlier setup: it adds
// every recorded one that is not in the Copilot app yet. It finishes even if the page is closed,
// so no turn is cut off halfway.
func (a *app) showImported() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	rt, err := a.manager.VerifyRuntime()
	if err != nil {
		return "", errors.New("Set up Copilot first, or choose Repair Copilot")
	}
	a.mu.Lock()
	a.imports = &importProgress{Running: true, Phase: "show", ShowInApp: true, Failures: []string{}}
	p := a.imports
	a.mu.Unlock()
	defer func() { a.mu.Lock(); p.Running = false; a.mu.Unlock() }()
	separate := 0
	for profile, ids := range sessions.OpenRecord(filepath.Join(a.root, "imported-sessions.json")).All() {
		if !samePath(profile, a.existingProfile()) {
			separate += len(ids) // the Copilot app doesn't read a separate profile, so indexing there would only run hooks
			continue
		}
		var jobs []showJob
		for external, id := range ids {
			if !sessionID.MatchString(id) {
				continue
			}
			if _, err := os.Stat(filepath.Join(profile, "session-state", id, "events.jsonl")); err != nil {
				continue // deleted in Copilot since
			}
			source, _, _ := strings.Cut(external, ":")
			title := id
			if ws, err := sessions.ReadWorkspace(profile, id); err == nil && ws.Name != "" {
				title = ws.Name
			}
			jobs = append(jobs, showJob{id: id, title: title, application: sessions.Product(source)})
		}
		if len(jobs) > 0 {
			a.showConversations(ctx, rt.Path, profile, jobs)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.ShowTotal == 0 {
		if separate > 0 {
			return "", errors.New("Your conversations were imported into a separate Copilot profile, which the Copilot app doesn't read. Run setup with your existing profile to use them in the app.")
		}
		return "", nil // none needed adding; thenOpenInApp says what there is to open
	}
	msg := fmt.Sprintf("%d of %d imported conversations are ready for the Copilot app.", p.Shown, p.ShowTotal)
	// Conversations added with a problem are listed too, not only those that weren't added.
	if len(p.Failures) > 0 {
		msg += " " + strings.Join(p.Failures, " ")
	}
	return msg, nil
}

// thenOpenInApp follows the maintenance action's indexing with opening the conversations in the
// app. When the app can't be opened, it says how to list them by hand instead.
func (a *app) thenOpenInApp(msg string) string {
	opened, err := a.openInApp(false)
	switch {
	case err != nil:
		opened = err.Error()
	case msg == "" && opened == noConversationsToOpen:
		return "There are no imported conversations to show. Conversations are imported when you run setup."
	}
	return strings.TrimSpace(msg + " " + opened)
}

// manualAppSteps is how a user lists the conversations without Open in the Copilot app.
const manualAppSteps = "Or list them yourself: add each repository in the app and set Settings → Sessions → Show Copilot CLI Session to a range that covers their dates, such as Last 360 days; restart the app if it was open. Conversations from a folder that isn't a Git repository appear only through Open in the Copilot app."
