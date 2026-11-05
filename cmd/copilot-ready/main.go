package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"copilotready/internal/core"
	"copilotready/internal/installer"
	"copilotready/internal/migration"
	"copilotready/internal/sessions"
)

var version = "2.0.6"
var releaseBaseURL = ""

//go:embed web/*
var web embed.FS

type options struct {
	Mode     string `json:"mode"`
	Project  string `json:"project"`
	Sandbox  string `json:"sandbox"`
	GitAuth  bool   `json:"gitAuth"`
	Baseline bool   `json:"baseline"`
}
type selection struct {
	IDs        []string `json:"ids"`
	Overwrites []string `json:"overwrites"`
}
type app struct {
	home, root, host, token string
	cursor                  bool // Cursor as a source, behind -cursor until tested with real installs
	manager                 *installer.Manager
	mu                      sync.Mutex
	busy                    bool
	report                  *migration.Report
	opts                    options
	changes                 []core.Change
	hooks                   []core.Change // the hook files within changes
	selected                []string
	overwrites              []string
	planID                  string
	result                  any
	imports                 *importProgress
	openMu                  sync.Mutex // one Open in the Copilot app at a time
}
type importProgress struct {
	Running bool `json:"running"`
	// Phase is "import", then "show" while conversations are added to the Copilot app, then
	// "finish" while hooks and plugins held back for that are installed.
	Phase    string `json:"phase"`
	Total    int    `json:"total"`
	Done     int    `json:"done"`
	Imported int    `json:"imported"`
	Skipped  int    `json:"skipped"`
	Failed   int    `json:"failed"`
	// ShowInApp counts conversations added to Copilot's session index, which the Copilot app lists.
	ShowInApp bool `json:"showInApp"`
	// Separate is a separate Copilot profile, which the Copilot app doesn't read.
	Separate   bool     `json:"separate,omitempty"`
	ShowTotal  int      `json:"showTotal"`
	ShowDone   int      `json:"showDone"`
	Shown      int      `json:"shown"`
	ShowFailed int      `json:"showFailed"`
	Failures   []string `json:"failures"`
}

func main() {
	// Command shims enter before setup flag parsing so Copilot receives its own
	// arguments unchanged, including --help and --version.
	if len(os.Args) > 1 && os.Args[1] == "--run-cli" {
		if len(os.Args) < 5 || os.Args[4] != "--" || !filepath.IsAbs(os.Args[2]) || !filepath.IsAbs(os.Args[3]) {
			fatal(errors.New("Copilot command registration needs repair; reopen setup"))
		}
		if err := installer.New(os.Args[2], os.Args[3]).RunCLI(os.Args[5:]); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				os.Exit(exit.ExitCode())
			}
			fatal(err)
		}
		return
	}
	homeDefault, _ := os.UserHomeDir()
	home := flag.String("home", homeDefault, "Home directory (isolated testing)")
	root := flag.String("root", "", "Managed installation directory")
	noBrowser := flag.Bool("no-browser", false, "Do not open a browser")
	launch := flag.Bool("launch", false, "Launch the saved project")
	flag.Bool("choose-project", false, "Open project selection")
	hookAdapter := flag.String("hook-adapter", "", "Run a reviewed context-only hook")
	showVersion := flag.Bool("version", false, "Print setup version")
	cursor := flag.Bool("cursor", os.Getenv("COPILOT_READY_CURSOR") == "1", "Also bring settings from Cursor (preview; or set COPILOT_READY_CURSOR=1)")
	flag.Parse()
	if *hookAdapter != "" {
		if err := runHook(*hookAdapter); err != nil {
			fatal(err)
		}
		return
	}
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *root == "" {
		*root = filepath.Join(*home, ".copilot-ready")
	}
	absHome, err := filepath.Abs(*home)
	if err != nil {
		fatal(err)
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fatal(err)
	}
	a := &app{home: absHome, root: absRoot, cursor: *cursor, manager: installer.New(absRoot, absHome)}
	if *launch {
		s, e := a.manager.Status()
		if e == nil && s.Launch != nil {
			if e = a.manager.Launch(*s.Launch); e == nil {
				return
			}
			fmt.Println("Launch needs attention:", e)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	a.host = listener.Addr().String()
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		fatal(err)
	}
	a.token = hex.EncodeToString(b)
	server := &http.Server{Handler: guard(a.host, a.token, a.routes()), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	url := "http://" + a.host
	fmt.Println("Copilot Ready", version, "— local setup")
	fmt.Println("Open this local link:", url)
	fmt.Println("Close this window when finished. Personal configuration stays on this computer.")
	if !*noBrowser {
		if err = installer.OpenBrowser(url); err != nil {
			fmt.Println("Browser could not open. Use the local link above.")
		}
	}
	// A setup session never becomes a permanent local service.
	go func() { time.Sleep(2 * time.Hour); _ = server.Close() }()
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "Copilot Ready:", err); os.Exit(1) }

func guard(host, token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != host || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Local access only", 403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Setup-Token")), []byte(token)) != 1 {
				http.Error(w, "Reopen the local setup page", 403)
				return
			}
			if r.Method != "GET" && r.Header.Get("Origin") != "http://"+host {
				http.Error(w, "Local origin required", 403)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, _ := web.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		sources, orCursor := "Codex and Claude Code", ""
		if a.cursor {
			sources, orCursor = "Codex, Claude Code and Cursor", ", or Cursor"
		}
		_, _ = w.Write([]byte(strings.NewReplacer("__TOKEN__", a.token, "__SOURCES__", sources, "__OR_CURSOR__", orCursor).Replace(string(b))))
	})
	assets, _ := fs.Sub(web, "web")
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		status, err := a.manager.Status()
		warn := ""
		if err != nil {
			warn = err.Error()
		}
		respond(w, map[string]any{"current": currentSettings(a.existingProfile()), "version": version, "root": a.root, "existingProfile": a.existingProfile(), "prerequisites": installer.Prerequisites(), "status": status, "warning": warn, "busy": a.busy, "result": a.result, "report": a.report, "options": a.opts, "imports": a.imports})
	})
	mux.HandleFunc("POST /api/folder", a.action(func(w http.ResponseWriter, r *http.Request) error {
		p, err := installer.PickFolder()
		if err != nil {
			return err
		}
		respond(w, map[string]string{"path": p})
		return nil
	}))
	mux.HandleFunc("POST /api/scan", a.action(a.scan))
	mux.HandleFunc("POST /api/preview", a.action(a.preview))
	mux.HandleFunc("POST /api/apply", a.action(a.apply))
	mux.HandleFunc("POST /api/launch", a.action(func(w http.ResponseWriter, r *http.Request) error {
		s, err := a.manager.Status()
		if err != nil {
			return err
		}
		if s.Launch == nil {
			return errors.New("Finish setup before launching")
		}
		if err = a.manager.Launch(*s.Launch); err != nil {
			return err
		}
		respond(w, map[string]string{"message": "Copilot opened. Sign in, review folder trust, and check sandbox status in its native interface."})
		return nil
	}))
	mux.HandleFunc("POST /api/powershell", a.action(func(w http.ResponseWriter, r *http.Request) error {
		if err := installer.InstallPowerShell(r.Context()); err != nil {
			return err
		}
		respond(w, map[string]string{"message": "PowerShell installation completed. Scan again to refresh prerequisites."})
		return nil
	}))
	mux.HandleFunc("POST /api/maintenance", a.action(a.maintenance))
	mux.HandleFunc("POST /api/path", a.action(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Confirm bool `json:"confirm"`
		}
		if err := decode(r, &req); err != nil {
			return err
		}
		if !req.Confirm {
			return errors.New("Confirm adding the owned launcher directory to your user PATH")
		}
		if err := a.manager.AddToPath(); err != nil {
			return err
		}
		respond(w, map[string]string{"message": "The copilot command is available in new terminal windows. Existing Copilot commands keep their precedence."})
		return nil
	}))
	return mux
}
func (a *app) action(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		if a.busy {
			a.mu.Unlock()
			http.Error(w, "Another setup action is running", 409)
			return
		}
		a.busy = true
		a.mu.Unlock()
		defer func() { a.mu.Lock(); a.busy = false; a.mu.Unlock() }()
		if err := fn(w, r); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		}
	}
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("Unexpected extra input")
	}
	return nil
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (a *app) existingProfile() string {
	if v := os.Getenv("COPILOT_HOME"); v != "" {
		return v
	}
	return filepath.Join(a.home, ".copilot")
}
func (a *app) destination(o options) string {
	if o.Mode == "existing" {
		return a.existingProfile()
	}
	return filepath.Join(a.root, "profiles", "default")
}
func (a *app) scan(w http.ResponseWriter, r *http.Request) error {
	var o options
	if err := decode(r, &o); err != nil {
		return err
	}
	if o.Mode != "existing" && o.Mode != "new" {
		return errors.New("Choose existing or new profile")
	}
	if o.Sandbox != "manual" && o.Sandbox != "sandbox" && (o.Sandbox != "preserve" || o.Mode != "existing") {
		return errors.New("Choose how Copilot runs")
	}
	pre := installer.Prerequisites()
	if o.Sandbox == "sandbox" && pre.SandboxEligibilityKnown && !pre.SandboxEligible {
		return errors.New("This operating-system version does not meet sandbox prerequisites. Choose manual mode explicitly, or update the operating system before continuing")
	}
	if !filepath.IsAbs(o.Project) {
		return errors.New("Choose an existing project folder")
	}
	info, err := os.Stat(o.Project)
	if err != nil || !info.IsDir() {
		return errors.New("Project folder does not exist")
	}
	report, err := migration.Discover(migration.Options{Home: a.home, Project: o.Project, Destination: a.destination(o), Mode: o.Mode, CodexHome: os.Getenv("CODEX_HOME"), ClaudeHome: os.Getenv("CLAUDE_CONFIG_DIR"), CopilotHome: a.existingProfile(), SessionRecord: filepath.Join(a.root, "imported-sessions.json"), CodexExecutable: sessions.CodexExecutable(a.home), Cursor: a.cursor})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.report = &report
	a.opts = o
	a.changes = nil
	a.planID = ""
	a.result = nil
	a.mu.Unlock()
	respond(w, report)
	return nil
}
func snapshot(path string) ([]byte, string, error) {
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil, "absent", nil
	}
	if e != nil {
		return nil, "", e
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}
func (a *app) preview(w http.ResponseWriter, r *http.Request) error {
	var s selection
	if err := decode(r, &s); err != nil {
		return err
	}
	if a.report == nil {
		return errors.New("Scan first")
	}
	a.changes = nil
	a.planID = ""
	changes, err := migration.BuildChanges(*a.report, s.IDs, s.Overwrites)
	if err != nil {
		return err
	}
	dest := a.destination(a.opts)
	settingsPath := filepath.Join(dest, "settings.json")
	original, before, err := snapshot(settingsPath)
	if err != nil {
		return err
	}
	current := original
	for i, c := range changes {
		if c.Path == settingsPath {
			original = c.Content
			before = c.BeforeSHA256
			changes = append(changes[:i], changes[i+1:]...)
			break
		}
	}
	settings, err := patchSettings(original, a.opts.Mode == "new", a.opts.Baseline, a.opts.Sandbox, a.opts.GitAuth)
	if err != nil {
		return err
	}
	settingsDiff := settingChanges(current, settings)
	if len(settingsDiff) > 0 {
		changes = append(changes, core.Change{Path: settingsPath, Content: settings, BeforeSHA256: before, Mode: 0600, Source: "Reviewed setup preferences; comments and unrelated settings retained"})
	}
	// Approved hooks are written in this same reviewed transaction; plugins install after it.
	var hooks []migration.Activation
	plugins := 0
	for _, id := range s.IDs {
		if activation, err := migration.GetActivation(*a.report, id); err == nil {
			switch activation.Kind {
			case "hook":
				hooks = append(hooks, activation)
			case "plugin":
				plugins++
			}
		}
	}
	hookFiles, err := a.hookChanges(dest, hooks)
	if err != nil {
		return err
	}
	changes = append(changes, hookFiles...)
	helper, err := os.Executable()
	if err != nil {
		return err
	}
	launchers, err := a.manager.PrepareLaunchers(helper, installer.LaunchOptions{Profile: dest, Project: a.opts.Project, Sandbox: a.opts.Sandbox, GitAuth: a.opts.GitAuth, Normal: a.opts.Mode == "existing"})
	if err != nil {
		return err
	}
	changes = append(changes, launchers...)
	planToken := make([]byte, 16)
	if _, err = rand.Read(planToken); err != nil {
		return err
	}
	a.mu.Lock()
	a.changes = changes
	a.hooks = hookFiles
	a.selected = append([]string(nil), s.IDs...)
	a.overwrites = append([]string(nil), s.Overwrites...)
	a.planID = hex.EncodeToString(planToken)
	a.mu.Unlock()
	views := []map[string]any{}
	for _, c := range changes {
		action := "Update with backup"
		if c.BeforeSHA256 == "absent" {
			action = "Create"
		}
		views = append(views, map[string]any{"path": c.Path, "source": c.Source, "action": action, "bytes": len(c.Content)})
	}
	conversations := 0
	for _, id := range s.IDs {
		conversations += len(migration.Conversations(*a.report, id))
	}
	respond(w, map[string]any{"planId": a.planID, "changes": views, "profile": dest, "mode": a.opts.Mode, "sandbox": a.opts.Sandbox, "baseline": a.opts.Baseline, "gitAuth": a.opts.GitAuth, "conversations": conversations, "plugins": plugins, "settings": settingsDiff})
	return nil
}

func sameSelection(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func (a *app) apply(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		PlanID    string  `json:"planId"`
		Options   options `json:"options"`
		ShowInApp bool    `json:"showInApp"`
		selection
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if a.changes == nil || a.planID == "" || req.PlanID != a.planID || req.Options != a.opts || !sameSelection(req.IDs, a.selected) || !sameSelection(req.Overwrites, a.overwrites) {
		return errors.New("Your choices changed or this review expired. Review the changes again before setting up Copilot")
	}
	if err := a.waitForImports(); err != nil {
		return err
	}
	// A separate profile is one the Copilot app doesn't read, so there is nothing to show it.
	if a.opts.Mode == "new" {
		req.ShowInApp = false
	}
	prereq := installer.Prerequisites()
	if !prereq.Supported {
		return errors.New("This release supports Windows and macOS only")
	}
	if prereq.NeedsPowerShell {
		return errors.New("Install the required PowerShell runtime using the button above, then retry")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	rt, err := a.manager.EnsureRuntime(ctx)
	if err != nil {
		return err
	}
	profile := a.destination(a.opts)
	var conversations []sessions.Session
	var plugins []string
	for _, id := range a.selected {
		conversations = append(conversations, migration.Conversations(*a.report, id)...)
	}
	for _, item := range a.report.Items {
		if item.ActivationKind == "plugin" && slices.Contains(a.selected, item.ID) {
			plugins = append(plugins, item.ID)
		}
	}
	// Showing conversations in the Copilot app runs one Copilot turn for each. Hooks and plugins
	// imported now would run during those turns, so they wait until the turns are done.
	later := afterShowing{report: a.report, project: a.opts.Project}
	changes := a.changes
	if req.ShowInApp && len(conversations) > 0 {
		later.hooks, later.plugins, plugins = a.hooks, plugins, nil
		changes = slices.DeleteFunc(slices.Clone(a.changes), func(c core.Change) bool {
			return slices.ContainsFunc(a.hooks, func(h core.Change) bool { return h.Path == c.Path })
		})
	}
	// All paths originate in our migration/launcher builders, never browser-supplied changes.
	roots := append([]string{profile}, a.manager.LauncherRoots()...)
	later.roots = roots
	tx, err := a.manager.Apply(changes, roots)
	if err != nil {
		return err
	}
	_ = a.manager.MarkSetup(tx.ID)
	a.changes = nil
	a.planID = ""
	validation := append(a.validate(ctx, rt, profile, a.opts.Project), a.installPlugins(ctx, a.report, rt.Path, profile, a.opts.Project, plugins)...)
	if err := a.manager.AddToPath(); err != nil {
		validation = append(validation, map[string]string{"name": "Terminal command", "status": "needs attention", "detail": "Your changes are applied, but the copilot command could not be registered: " + err.Error()})
	}
	result := map[string]any{"message": "Setup complete. Your selected changes are applied.", "transaction": tx, "validation": validation, "profile": profile, "mode": a.opts.Mode, "runtime": rt}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.imports = nil
	if len(conversations) > 0 {
		a.imports = &importProgress{Running: true, Phase: "import", Total: len(conversations), ShowInApp: req.ShowInApp, Separate: a.opts.Mode == "new", Failures: []string{}}
		// The result shares the live progress, so a reopened setup shows how the import ended.
		result["imports"] = a.imports
		go a.importConversations(rt.Path, profile, a.report.Options, conversations, req.ShowInApp, later)
	}
	a.result = result
	respond(w, result)
	return nil
}

// installPlugins installs approved plugins with Copilot's own command once the files are in
// place. A plugin that fails is reported with the setup checks and does not stop the rest.
func (a *app) installPlugins(ctx context.Context, report *migration.Report, copilot, profile, project string, ids []string) []map[string]string {
	var checks []map[string]string
	for _, item := range report.Items {
		if item.ActivationKind != "plugin" || !slices.Contains(ids, item.ID) {
			continue
		}
		check := map[string]string{"name": "Plugin " + item.Name, "status": "passed", "detail": "Installed in Copilot."}
		plugin, err := migration.GetActivation(*report, item.ID)
		if err == nil {
			err = installPlugin(ctx, copilot, a.manager.Environment(profile), project, plugin)
		}
		if err != nil {
			check["status"], check["detail"] = "needs attention", "Not installed: "+err.Error()+"."
		}
		checks = append(checks, check)
	}
	return checks
}

// afterShowing is setup work held back until conversations are in the Copilot app.
type afterShowing struct {
	report  *migration.Report
	hooks   []core.Change
	plugins []string
	roots   []string
	project string
}

var errAlreadyImported = errors.New("already imported")

// waitForImports refuses actions that would collide with a conversation import in progress.
func (a *app) waitForImports() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.imports != nil && a.imports.Running {
		return errors.New("Your conversations are still importing. Try again when they finish")
	}
	return nil
}

// forgetSetup drops the finished setup after Undo or Uninstall, so reopening setup does not
// show a completed screen for it.
func (a *app) forgetSetup() {
	a.mu.Lock()
	a.result = nil
	a.mu.Unlock()
}

// importConversations runs after setup completes, so Copilot can open while it works. Each
// conversation is converted and handed to `copilot sessions import`, then, when asked, added to
// the Copilot app's list. A failure is reported and never blocks or undoes the setup.
func (a *app) importConversations(copilot, profile string, o migration.Options, list []sessions.Session, show bool, later afterShowing) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	defer func() { a.mu.Lock(); a.imports.Running = false; a.mu.Unlock() }()
	record := sessions.OpenRecord(o.SessionRecord)
	a.importAll(ctx, copilot, profile, o, record, list)
	if show {
		var jobs []showJob
		for _, s := range list {
			if id, ok := record.Get(profile, s.ExternalID()); ok {
				jobs = append(jobs, showJob{id: id, title: s.Title, application: sessions.Product(s.Source)})
			}
		}
		a.showConversations(ctx, copilot, profile, jobs)
	}
	a.finishLater(ctx, copilot, profile, later)
}

func (a *app) importAll(ctx context.Context, copilot, profile string, o migration.Options, record *sessions.Record, list []sessions.Session) {
	finish := func(s sessions.Session, err error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		p := a.imports
		p.Done++
		switch {
		case err == nil:
			p.Imported++
		case errors.Is(err, sessions.ErrEmpty), errors.Is(err, errAlreadyImported):
			p.Skipped++
		default:
			p.Failed++
			if len(p.Failures) < 50 {
				p.Failures = append(p.Failures, fmt.Sprintf("Not imported: %s (%s): %v", s.Title, sessions.Product(s.Source), err))
			}
		}
	}
	dir, err := os.MkdirTemp(a.root, "conversations-")
	if err != nil {
		for _, s := range list {
			finish(s, err)
		}
		return
	}
	defer os.RemoveAll(dir)
	importer := sessions.Importer{Copilot: copilot, Env: a.manager.Environment(profile), Profile: profile, Dir: dir}
	var codex *sessions.Codex
	codexErr := sessions.ErrNoCodex
	var startCodex sync.Once
	defer func() {
		if codex != nil {
			codex.Close()
		}
	}()
	jobs := make(chan sessions.Session)
	var workers sync.WaitGroup
	for range 6 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for s := range jobs {
				// Another batch or setup window may have imported it since the review.
				if record.Has(profile, s.ExternalID()) {
					finish(s, errAlreadyImported)
					continue
				}
				var c sessions.Conversation
				var err error
				if s.Source == "claude" {
					c, err = sessions.LoadClaude(s)
				} else if s.Source == "cursor" {
					c, err = sessions.LoadCursor(s)
				} else {
					startCodex.Do(func() {
						if o.CodexExecutable != "" {
							codex, codexErr = sessions.StartCodex(ctx, o.CodexExecutable, o.CodexHome)
						}
					})
					if err = codexErr; err == nil {
						c, err = codex.Load(s)
					}
				}
				var id string
				if err == nil {
					id, err = importer.Import(ctx, c)
				}
				if err == nil {
					_ = record.Add(profile, s.ExternalID(), id)
				}
				finish(s, err)
			}
		}()
	}
	for _, s := range list {
		jobs <- s
	}
	close(jobs)
	workers.Wait()
}

func (a *app) validate(ctx context.Context, rt installer.Runtime, profile, project string) []map[string]string {
	checks := []map[string]string{}
	for _, check := range []struct {
		name string
		args []string
	}{{"Configuration", []string{"config", "--list"}}, {"Instructions", []string{"instruction", "list", "--json"}}, {"Skills", []string{"skill", "list", "--json"}}} {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cmd := exec.CommandContext(callCtx, rt.Path, append([]string{"--no-auto-update"}, check.args...)...)
		cmd.Env = a.manager.Environment(profile)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		cancel()
		state := "passed"
		detail := "Native CLI check completed."
		if err != nil {
			state = "needs attention"
			detail = redactCommandOutput(string(out))
		}
		checks = append(checks, map[string]string{"name": check.name, "status": state, "detail": detail})
	}
	checks = append(checks, map[string]string{"name": "Sign-in, selected model, sandbox enforcement, and Git authentication", "status": "not verified", "detail": "Complete these checks in Copilot before relying on the environment. Discovery does not prove enforcement or model behavior."})
	return checks
}
func (a *app) maintenance(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Action  string `json:"action"`
		ID      string `json:"id"`
		Confirm bool   `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if !req.Confirm {
		return errors.New("Confirm this maintenance action")
	}
	// Undo and Uninstall would reset the screen and its import progress, and Uninstall removes
	// the runtime the import is using. Showing conversations in the app works on the imports.
	if req.Action == "undo" || req.Action == "uninstall" || req.Action == "show" || req.Action == "open" {
		if err := a.waitForImports(); err != nil {
			return err
		}
	}
	var err error
	switch req.Action {
	case "repair":
		_, err = a.manager.RepairRuntime(r.Context())
	case "rollback":
		err = a.manager.Rollback()
	case "recover":
		err = a.manager.Recover(req.ID)
	case "undo":
		if err = a.manager.UndoSetup(); err == nil {
			a.forgetSetup()
			respond(w, map[string]string{"message": "Setup changes were undone. Backed-up files are restored and files setup created are removed. Imported conversations and plugins installed by Copilot stay; remove a plugin with copilot plugin uninstall."})
			return nil
		}
	case "show":
		var msg string
		if msg, err = a.showImported(); err == nil {
			respond(w, map[string]string{"message": a.thenOpenInApp(msg)})
			return nil
		}
	case "open":
		var msg string
		if msg, err = a.openInApp(true); err == nil {
			respond(w, map[string]string{"message": msg})
			return nil
		}
	case "uninstall":
		if err = a.manager.Uninstall(); err == nil {
			a.forgetSetup()
		}
	case "update":
		respond(w, map[string]string{"url": releaseBaseURL, "message": "Download the latest reviewed setup from the website. Rerunning it upgrades the managed runtime and previews template changes."})
		return nil
	default:
		return errors.New("Unknown maintenance action")
	}
	if err != nil {
		return err
	}
	respond(w, map[string]string{"message": "Maintenance completed. Profiles and project files were preserved unless restored by the selected transaction."})
	return nil
}
