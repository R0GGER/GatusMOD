// Package app wires the GatusMOD components together into one HTTP handler.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"

	"gatusmod/internal/admin"
	"gatusmod/internal/badge"
	"gatusmod/internal/config"
	"gatusmod/internal/probe"
	"gatusmod/internal/proxy"
	"gatusmod/internal/related"
	"gatusmod/internal/settings"
	"gatusmod/internal/statuspage"
	"gatusmod/internal/store"
	"gatusmod/internal/syncer"
	"gatusmod/web"
)

type App struct {
	Settings *settings.Manager
	Syncer   *syncer.Syncer
	Tracker  *proxy.Tracker
	Probes   *probe.Runner
	admin    http.Handler
	badges   http.Handler
	api      http.Handler
	push     http.Handler
	// statusPages keeps the logins of the private status pages across configuration changes.
	statusPages *statuspage.Pages
	handler     atomic.Pointer[http.Handler]
}

// New wires everything together. dataDir holds the admin settings file.
func New(watcher *config.Watcher, dataDir string) (*App, error) {
	mgr, err := settings.Open(dataDir, watcher.Get())
	if err != nil {
		return nil, fmt.Errorf("loading the admin settings: %w", err)
	}
	a := &App{Settings: mgr, Tracker: proxy.NewTracker(), Probes: probe.NewRunner(mgr.Get), statusPages: statuspage.New()}
	history, err := probe.OpenHistory(filepath.Join(dataDir, "iperf3-history.json"))
	if err != nil {
		slog.Warn("cannot read the iperf3 test history; starting with an empty one", "error", err)
	}
	a.Probes.UseHistory(history)
	a.Syncer = syncer.New(mgr.Get)
	a.Syncer.OnRecords(func(records []store.Record) { a.Probes.Update(syncer.Targets(records)) })
	api := http.NewServeMux()
	api.Handle("GET /mod/api/endpoints/{key}/related", related.NewIndex(mgr.Get, a.Syncer.Records).Handler())
	api.Handle("GET /mod/api/endpoints/{key}/iperf3", a.Probes.IPerf3Handler())
	a.api = api
	a.push = a.Probes.PushHandler()
	a.admin = admin.New(admin.Deps{Config: mgr.Get, Settings: mgr, Syncer: a.Syncer, Tracker: a.Tracker, Probes: a.Probes}).Handler()
	a.badges = badge.New(badge.Deps{Config: mgr.Get, Records: a.Syncer.Records}).Routes()
	if err := a.rebuild(mgr.Get()); err != nil {
		return nil, err
	}
	watcher.OnChange(mgr.SetBase)
	mgr.OnChange(func(cfg *config.Config) {
		if err := a.rebuild(cfg); err != nil {
			slog.Error("cannot apply the new configuration", "error", err)
			return
		}
		a.Syncer.Trigger()
		go proxy.SelfTest(context.Background(), a, a.Tracker)
	})
	return a, nil
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) { (*a.handler.Load()).ServeHTTP(w, r) }

func (a *App) rebuild(cfg *config.Config) error {
	p, err := proxy.New(cfg, a.Tracker)
	if err != nil {
		return err
	}
	p.HideDisabled(a.disabledKeys)
	mux := http.NewServeMux()
	mux.Handle("/mod/static/", http.StripPrefix("/mod/static/", http.FileServerFS(web.Static())))
	mux.HandleFunc("/mod/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/mod/api/", a.api)
	mux.Handle(probe.PushPrefix, a.push)
	mux.Handle(admin.Prefix, a.admin)
	mux.Handle(admin.Prefix+"/", a.admin)
	mux.Handle(badge.Prefix+"/", a.badges)
	mux.Handle("/images/", http.StripPrefix("/images/", noDirListing(http.FileServer(http.Dir(cfg.ImagesDir)))))
	mux.Handle("/favicon/", http.StripPrefix("/favicon/", noDirListing(http.FileServer(http.Dir(cfg.FaviconDir)))))
	mux.Handle("/", publicFiles(cfg.PublicDir, p))
	h := a.statusPages.Wrap(cfg, mux)
	a.handler.Store(&h)
	return nil
}

// disabledKeys are the Gatus keys of the generated endpoints that are disabled.
func (a *App) disabledKeys() map[string]bool {
	var keys map[string]bool
	for _, r := range a.Syncer.Records() {
		if r.Disabled {
			if keys == nil {
				keys = map[string]bool{}
			}
			keys[config.EndpointKey(r.Group, r.Name)] = true
		}
	}
	return keys
}

func noDirListing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// publicFiles serves files from dir at the root of the site (e.g. /favicon_256.png) and proxies everything else.
func publicFiles(dir string, next http.Handler) http.Handler {
	if dir == "" {
		return next
	}
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			clean := path.Clean("/" + r.URL.Path)
			if clean != "/" {
				if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(clean))); err == nil && !info.IsDir() {
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
