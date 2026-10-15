// Package admin serves the GatusMOD admin pages under /mod/admin.
package admin

import (
	"context"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"gatusmod/internal/badge"
	"gatusmod/internal/config"
	"gatusmod/internal/importer"
	"gatusmod/internal/probe"
	"gatusmod/internal/proxy"
	"gatusmod/internal/session"
	"gatusmod/internal/settings"
	"gatusmod/internal/store"
	"gatusmod/internal/syncer"
	"gatusmod/web"
)

const (
	Prefix        = "/mod/admin"
	maxImportSize = 5 << 20
)

type settingsT = settings.Settings

type Deps struct {
	Config   func() *config.Config
	Settings *settings.Manager
	Syncer   *syncer.Syncer
	Tracker  *proxy.Tracker
	Probes   *probe.Runner
	Client   *http.Client
}

type Admin struct {
	deps     Deps
	pages    map[string]*template.Template
	login    *template.Template
	sessions *session.Store
}

var templateFuncs = template.FuncMap{
	"since": func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		return t.Format("2006-01-02 15:04:05") + " (" + time.Since(t).Round(time.Second).String() + " ago)"
	},
	"when": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02 15:04:05")
	},
	"ms":     func(d time.Duration) int64 { return d.Milliseconds() },
	"join":   strings.Join,
	"redact": probe.Redact,
}

func New(deps Deps) *Admin {
	if deps.Client == nil {
		deps.Client = &http.Client{Timeout: 15 * time.Second}
	}
	pages := map[string]*template.Template{}
	for _, page := range []string{"overview.html", "checks.html", "databases.html", "database_edit.html", "view_edit.html", "tables.html", "table_edit.html", "settings.html", "statuspages.html", "import.html", "badges.html"} {
		pages[page] = template.Must(template.New("").Funcs(templateFuncs).ParseFS(web.Templates(), "layout.html", "partials.html", page))
	}
	login := template.Must(template.New("").Funcs(templateFuncs).ParseFS(web.Templates(), "login.html"))
	return &Admin{deps: deps, pages: pages, login: login, sessions: session.NewStore()}
}

func (a *Admin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Prefix+"/login", a.loginForm)
	mux.HandleFunc("POST "+Prefix+"/login", a.loginSubmit)
	mux.HandleFunc("POST "+Prefix+"/logout", a.logout)
	mux.HandleFunc("GET "+Prefix+"/{$}", a.overview)
	mux.HandleFunc("POST "+Prefix+"/sync", a.syncNow)
	mux.HandleFunc("GET "+Prefix+"/checks", a.checks)
	mux.HandleFunc("GET "+Prefix+"/databases", a.databasesList)
	mux.HandleFunc("GET "+Prefix+"/databases/new", a.databaseNew)
	mux.HandleFunc("GET "+Prefix+"/databases/{id}", a.databaseEdit)
	mux.HandleFunc("POST "+Prefix+"/databases/save", a.databaseSave)
	mux.HandleFunc("POST "+Prefix+"/databases/{id}/delete", a.databaseDelete)
	mux.HandleFunc("POST "+Prefix+"/databases/files/delete", a.orphanDelete)
	mux.HandleFunc("GET "+Prefix+"/databases/{id}/views/new", a.viewNew)
	mux.HandleFunc("GET "+Prefix+"/databases/{id}/views/{vid}", a.viewEdit)
	mux.HandleFunc("POST "+Prefix+"/databases/{id}/views/save", a.viewSave)
	mux.HandleFunc("POST "+Prefix+"/databases/{id}/views/{vid}/run", a.viewRun)
	mux.HandleFunc("POST "+Prefix+"/databases/{id}/views/{vid}/delete", a.viewDelete)
	mux.Handle("GET "+Prefix+"/imports", http.RedirectHandler(Prefix+"/databases", http.StatusMovedPermanently))
	mux.Handle("GET "+Prefix+"/imports/new", http.RedirectHandler(Prefix+"/databases", http.StatusMovedPermanently))
	mux.HandleFunc("GET "+Prefix+"/imports/{id}", a.legacyImport)
	mux.HandleFunc("GET "+Prefix+"/endpoints", a.tablesList)
	mux.HandleFunc("GET "+Prefix+"/endpoints/new", a.tableNew)
	mux.HandleFunc("GET "+Prefix+"/endpoints/{id}", a.tableEdit)
	mux.HandleFunc("POST "+Prefix+"/endpoints/save", a.tableSave)
	mux.HandleFunc("POST "+Prefix+"/endpoints/{id}/delete", a.tableDelete)
	mux.HandleFunc("GET "+Prefix+"/settings", a.settingsForm)
	mux.HandleFunc("POST "+Prefix+"/settings", a.settingsSave)
	mux.HandleFunc("POST "+Prefix+"/settings/reset", a.settingsReset)
	mux.HandleFunc("POST "+Prefix+"/settings/password", a.changePassword)
	mux.HandleFunc("POST "+Prefix+"/settings/example/delete", a.exampleDelete)
	mux.HandleFunc("GET "+Prefix+"/status-pages", a.statusPagesList)
	mux.HandleFunc("POST "+Prefix+"/status-pages", a.statusPageSave)
	mux.HandleFunc("POST "+Prefix+"/status-pages/{id}/delete", a.statusPageDelete)
	mux.HandleFunc("GET "+Prefix+"/import", a.yamlForm)
	mux.HandleFunc("POST "+Prefix+"/import", a.yamlSubmit)
	mux.HandleFunc("POST "+Prefix+"/import/export", a.backupExport)
	mux.HandleFunc("POST "+Prefix+"/import/restore", a.backupRestore)
	mux.HandleFunc("GET "+Prefix+"/badges", a.badges)
	mux.Handle("GET "+Prefix, http.RedirectHandler(Prefix+"/", http.StatusMovedPermanently))
	return a.protect(mux)
}

func (a *Admin) render(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	data["Page"] = page
	if info, ok := sessionFrom(r); ok {
		data["CSRF"] = info.CSRF
		data["User"] = info.User
	}
	if _, ok := data["Message"]; !ok {
		data["Message"] = r.URL.Query().Get("msg")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.pages[page].ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("rendering admin page", "page", page, "error", err)
	}
}

// redirect sends the browser to an admin page with a notice (post/redirect/get).
func redirect(w http.ResponseWriter, r *http.Request, path, msg string) {
	target := Prefix + path
	if msg != "" {
		target += "?msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

type patchRow struct {
	Resource string
	Entry    proxy.Entry
}

type importRow struct {
	config.Import
	Status   syncer.ImportStatus
	Schedule string
	Next     time.Time
	Label    string
	FileName string
}

func (a *Admin) importRows(cfg *config.Config) []importRow {
	status := a.deps.Syncer.Status()
	now := time.Now()
	rows := make([]importRow, 0, len(cfg.Imports))
	for _, imp := range cfg.Imports {
		row := importRow{
			Import:   imp,
			Status:   status.Imports[imp.ID],
			Schedule: imp.ScheduleText(cfg.Defaults.Interval),
			Next:     imp.NextRun(cfg.Defaults.Interval, now),
			Label:    imp.Label(),
		}
		if !imp.Local() {
			row.FileName = imp.FileName()
		}
		rows = append(rows, row)
	}
	return rows
}

func (a *Admin) overview(w http.ResponseWriter, r *http.Request) {
	cfg := a.deps.Config()
	snapshot := a.deps.Tracker.Snapshot()
	patches := make([]patchRow, 0, len(snapshot))
	for resource, entry := range snapshot {
		patches = append(patches, patchRow{resource, entry})
	}
	sort.Slice(patches, func(i, j int) bool { return patches[i].Resource < patches[j].Resource })
	records := a.deps.Syncer.Records()
	probes := a.probeRows(cfg, r)
	var attention []probeRow
	for _, p := range probes {
		if p.State() == "down" {
			attention = append(attention, p)
		}
	}
	a.render(w, r, "overview.html", map[string]any{
		"Config":       cfg,
		"Status":       a.deps.Syncer.Status(),
		"Imports":      a.importRows(cfg),
		"Summary":      summarize(records, probes),
		"Attention":    attention,
		"Patches":      patches,
		"SettingsPath": a.deps.Settings.Path(),
	})
}

func (a *Admin) checks(w http.ResponseWriter, r *http.Request) {
	cfg := a.deps.Config()
	records := a.deps.Syncer.Records()
	probes := a.probeRows(cfg, r)
	a.render(w, r, "checks.html", map[string]any{
		"Records": records,
		"Probes":  probes,
		"Summary": summarize(records, probes),
	})
}

type checkSummary struct {
	Endpoints, Enabled, Disabled int
	Probes, Up, Down, Pending    int
}

func summarize(records []store.Record, probes []probeRow) checkSummary {
	s := checkSummary{Endpoints: len(records), Probes: len(probes)}
	for _, r := range records {
		if r.Disabled {
			s.Disabled++
		} else {
			s.Enabled++
		}
	}
	for _, p := range probes {
		switch p.State() {
		case "up":
			s.Up++
		case "down":
			s.Down++
		default:
			s.Pending++
		}
	}
	return s
}

type probeRow struct {
	probe.Status
	Push    bool
	PushURL string
}

// State is "up", "down" or "pending"; a probe whose result Gatus rejected counts as down.
func (p probeRow) State() string {
	switch {
	case p.PushError != "" || (!p.Last.Time.IsZero() && !p.Last.Success):
		return "down"
	case p.Last.Time.IsZero():
		return "pending"
	}
	return "up"
}

func (a *Admin) probeRows(cfg *config.Config, r *http.Request) []probeRow {
	if a.deps.Probes == nil {
		return nil
	}
	base := badge.PublicBaseURL(cfg, r)
	statuses := a.deps.Probes.Statuses()
	rows := make([]probeRow, 0, len(statuses))
	for _, s := range statuses {
		row := probeRow{Status: s, Push: s.Target.Push()}
		if row.Push {
			row.PushURL = probe.PushURL(cfg, base, s.Target.Key)
		}
		rows = append(rows, row)
	}
	return rows
}

func (a *Admin) syncNow(w http.ResponseWriter, r *http.Request) {
	msg := "Sync finished."
	if err := a.deps.Syncer.SyncOnce(r.Context()); err != nil {
		msg = "Sync failed: " + err.Error()
	}
	redirect(w, r, "/", msg)
}

// yamlTargets are the imports a Gatus YAML file can be written to: they need columns for name and url.
func yamlTargets(cfg *config.Config) []config.Import {
	var out []config.Import
	for _, imp := range cfg.Imports {
		if !imp.Local() && imp.Fields.Name != "" && imp.Fields.URL != "" {
			out = append(out, imp)
		}
	}
	return out
}

func (a *Admin) yamlForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "import.html", a.importPage(r))
}

func (a *Admin) yamlSubmit(w http.ResponseWriter, r *http.Request) {
	data := a.importPage(r)
	targets := data["Targets"].([]config.Import)
	target := r.FormValue("target")
	defer func() { a.render(w, r, "import.html", data) }()
	var imp *config.Import
	for i := range targets {
		if targets[i].ID == target {
			imp = &targets[i]
		}
	}
	if imp == nil {
		data["Error"] = "Choose the import to write the endpoints to."
		return
	}
	content := []byte(r.FormValue("yaml"))
	if file, _, err := r.FormFile("file"); err == nil {
		content, err = io.ReadAll(file)
		file.Close()
		if err != nil {
			data["Error"] = "Reading the file failed: " + err.Error()
			return
		}
	}
	data["YAML"] = string(content)
	client, err := store.NewClient(imp)
	if err != nil {
		data["Error"] = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := importer.Import(ctx, store.NewRecordStore(client, imp.Fields), content)
	if err == nil || result.Created > 0 {
		data["Result"] = result
	}
	if result.Created > 0 || result.Updated > 0 {
		if err := a.deps.Syncer.RunImport(ctx, imp.ID, syncer.TriggerManual); err != nil {
			slog.Error("import failed", "import", imp.Name, "error", err)
		}
	}
	if err != nil {
		data["Error"] = err.Error()
	}
}
