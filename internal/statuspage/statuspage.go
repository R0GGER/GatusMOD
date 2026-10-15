// Package statuspage limits the dashboard to the groups and endpoints of a status page: strictly on the domains of
// the page, and as a filtered view at /status/{slug} on the main site. Private pages need a password, and their
// endpoints are hidden everywhere else.
package statuspage

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"path"
	"strings"

	"gatusmod/internal/config"
	"gatusmod/internal/session"
	"gatusmod/web"
)

const (
	// Header carries the slug of the page on the API requests of the dashboard, added by statuspage.js.
	Header = "X-GatusMOD-Status-Page"
	// QueryParam keeps the slug in the URL of the pages of the dashboard, so a reload stays on the status page.
	QueryParam = "status"
	PathPrefix = "/status/"
	LoginPath  = "/mod/status-login"
	LogoutPath = "/mod/status-logout"

	StatusesPath      = "/api/v1/endpoints/statuses"
	suiteStatusesPath = "/api/v1/suites/statuses"
	endpointsPrefix   = "/api/v1/endpoints/"
	modEndpoints      = "/mod/api/endpoints/"
)

// Scope is the status page a request belongs to. Page is nil on the main site when private pages hide endpoints.
type Scope struct {
	Page *config.StatusPage
	// Strict is set on a domain of the page: only its endpoints can be read there.
	Strict bool
	// Origin is the scheme and host the visitor used, e.g. https://status.example.com.
	Origin string
	cfg    *config.Config
}

// Visible reports whether the endpoint may be listed in this scope.
func (s *Scope) Visible(key string) bool {
	if s.Page != nil && !s.Page.Shows(key) {
		return false
	}
	return !s.cfg.HiddenKey(key, s.Page)
}

type contextKey struct{}

func FromContext(ctx context.Context) *Scope {
	s, _ := ctx.Value(contextKey{}).(*Scope)
	return s
}

func withScope(r *http.Request, s *Scope) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), contextKey{}, s))
}

// Pages serves the status pages. It keeps the logins of the private pages, so it lives as long as GatusMOD.
type Pages struct {
	sessions *session.Store
	login    *template.Template
}

func New() *Pages {
	return &Pages{
		sessions: session.NewStore(),
		login:    template.Must(template.ParseFS(web.Templates(), "status_login.html")),
	}
}

// Wrap resolves the status page of every request: by host (strict), by /status/{slug}, by the slug header of API
// requests, or by ?status= on the pages of the dashboard. A private page first needs a login. Requests without a
// status page go to next unchanged, except that endpoints of private pages cannot be read.
func (s *Pages) Wrap(cfg *config.Config, next http.Handler) http.Handler {
	if len(cfg.StatusPages) == 0 {
		return next
	}
	hidesEndpoints := cfg.HasPrivateStatusPages()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := cfg.StatusPageByHost(r.Host); p != nil {
			if s.routeLogin(w, r, cfg, p, true) {
				return
			}
			if p.Private() && !s.loggedIn(r, p) && !publicFile(r.URL.Path) {
				s.requireLogin(w, r, p, true)
				return
			}
			serveStrict(w, withScope(r, &Scope{Page: p, Strict: true, Origin: origin(r), cfg: cfg}), next)
			return
		}
		if s.routeLogin(w, r, cfg, nil, false) {
			return
		}
		if rest, ok := strings.CutPrefix(r.URL.Path, PathPrefix); ok {
			p := cfg.StatusPageBySlug(strings.TrimSuffix(rest, "/"))
			if p == nil || strings.Contains(strings.TrimSuffix(rest, "/"), "/") {
				http.NotFound(w, r)
				return
			}
			if p.Private() && !s.loggedIn(r, p) {
				s.requireLogin(w, r, p, false)
				return
			}
			// Gatus only knows / and the endpoint pages: serve its dashboard, statuspage.js moves the browser to /?status=.
			r = withScope(r, &Scope{Page: p, Origin: origin(r), cfg: cfg})
			u := *r.URL
			u.Path, u.RawPath = "/", ""
			r.URL = &u
			serveScoped(w, r, next)
			return
		}
		slug := r.Header.Get(Header)
		if slug == "" && isPage(r.URL.Path) {
			slug = r.URL.Query().Get(QueryParam)
		}
		if p := cfg.StatusPageBySlug(slug); p != nil {
			if p.Private() && !s.loggedIn(r, p) {
				s.requireLogin(w, r, p, false)
				return
			}
			serveScoped(w, withScope(r, &Scope{Page: p, Origin: origin(r), cfg: cfg}), next)
			return
		}
		if hidesEndpoints {
			serveScoped(w, withScope(r, &Scope{Origin: origin(r), cfg: cfg}), next)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isPage reports whether the path is a page of the Gatus dashboard rather than an API call or a file.
func isPage(p string) bool {
	return p == "/" || strings.HasPrefix(p, "/endpoints/") || strings.HasPrefix(p, "/suites/")
}

// publicFile reports whether a private page may serve the path before the login: the assets of the dashboard
// and GatusMOD, push URLs and the health check, but no data.
func publicFile(p string) bool {
	switch {
	case strings.HasPrefix(p, "/mod/static/"), strings.HasPrefix(p, "/mod/push/"), p == "/mod/health":
		return true
	case strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/mod/"), strings.HasPrefix(p, PathPrefix), p == "/metrics":
		return false
	}
	return path.Ext(p) != "" && path.Ext(p) != ".html"
}

// serveScoped serves the main site and the status pages on it. Endpoints the scope hides cannot be read.
func serveScoped(w http.ResponseWriter, r *http.Request, next http.Handler) {
	scope := FromContext(r.Context())
	p := r.URL.Path
	if p == suiteStatusesPath && scope.Page != nil {
		// Suites have no group of a status page.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
		return
	}
	hidden := false
	switch {
	case strings.HasPrefix(p, modEndpoints):
		hidden = scope.cfg.HiddenKey(keyOf(p, modEndpoints), scope.Page)
	case strings.HasPrefix(p, endpointsPrefix) && p != StatusesPath:
		// Results pushed with a token keep working.
		push := r.Method == http.MethodPost && strings.HasSuffix(p, "/external")
		hidden = !push && scope.cfg.HiddenKey(keyOf(p, endpointsPrefix), scope.Page)
	}
	if hidden {
		http.NotFound(w, r)
		return
	}
	next.ServeHTTP(w, r)
}

// serveStrict only lets through what a visitor of the status page needs, and only for the endpoints of the page.
func serveStrict(w http.ResponseWriter, r *http.Request, next http.Handler) {
	scope := FromContext(r.Context())
	p := r.URL.Path
	allowed := true
	switch {
	case p == "/mod/admin" || strings.HasPrefix(p, "/mod/admin/"), strings.HasPrefix(p, "/mod/badges/"), strings.HasPrefix(p, PathPrefix), p == "/metrics":
		allowed = false
	case strings.HasPrefix(p, modEndpoints):
		allowed = scope.Visible(keyOf(p, modEndpoints))
	case p == StatusesPath, p == suiteStatusesPath, p == "/api/v1/config":
		allowed = r.Method == http.MethodGet || r.Method == http.MethodHead
	case strings.HasPrefix(p, endpointsPrefix):
		allowed = (r.Method == http.MethodGet || r.Method == http.MethodHead) && scope.Visible(keyOf(p, endpointsPrefix))
	case strings.HasPrefix(p, "/api/"):
		allowed = false
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	serveScoped(w, r, next)
}

func keyOf(path, prefix string) string {
	key, _, _ := strings.Cut(strings.TrimPrefix(path, prefix), "/")
	return key
}

// FilterStatuses keeps the endpoints that are visible in a response of /api/v1/endpoints/statuses.
func FilterStatuses(body []byte, visible func(key string) bool) ([]byte, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return body, err
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var ep struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(item, &ep) == nil && visible(ep.Key) {
			kept = append(kept, item)
		}
	}
	return json.Marshal(kept)
}

func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "http" || proto == "https" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}
