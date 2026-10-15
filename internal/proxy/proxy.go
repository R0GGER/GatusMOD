// Package proxy forwards requests to Gatus and patches the responses that GatusMOD customizes.
package proxy

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"

	"gatusmod/internal/config"
	"gatusmod/internal/inject"
	"gatusmod/internal/statuspage"
)

const (
	appJSPath    = "/js/app.js"
	manifestPath = "/manifest.json"
	maxBodySize  = 32 << 20
)

type Proxy struct {
	cfg     *config.Config
	rp      *httputil.ReverseProxy
	tracker *Tracker
	// disabled returns the keys of the disabled endpoints. Gatus keeps listing them with their last results,
	// so they are left out of the dashboard.
	disabled func() map[string]bool
}

func New(cfg *config.Config, tracker *Tracker) (*Proxy, error) {
	upstream, err := url.Parse(cfg.Upstream)
	if err != nil || upstream.Scheme == "" || upstream.Host == "" {
		return nil, fmt.Errorf("invalid upstream %q", cfg.Upstream)
	}
	p := &Proxy{cfg: cfg, tracker: tracker}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
			if p.mayRewrite(pr.In) {
				// The response must be uncompressed and complete (not a 304) to be patched.
				pr.Out.Header.Del("Accept-Encoding")
				pr.Out.Header.Del("If-None-Match")
				pr.Out.Header.Del("If-Modified-Since")
			}
		},
		ModifyResponse: p.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("upstream request failed", "path", r.URL.Path, "error", err)
			http.Error(w, "Gatus is not reachable", http.StatusBadGateway)
		},
	}
	return p, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) { p.rp.ServeHTTP(w, r) }

// HideDisabled leaves the endpoints with the keys fn returns out of the endpoint statuses.
func (p *Proxy) HideDisabled(fn func() map[string]bool) { p.disabled = fn }

func (p *Proxy) disabledKeys() map[string]bool {
	if p.disabled == nil {
		return nil
	}
	return p.disabled()
}

// mayRewrite reports whether the request can return a response we patch: SPA pages (no extension), app.js,
// the manifest, or the endpoint statuses of a status page or of a main site that hides private endpoints.
func (p *Proxy) mayRewrite(r *http.Request) bool {
	urlPath := r.URL.Path
	switch urlPath {
	case statuspage.StatusesPath:
		return statuspage.FromContext(r.Context()) != nil || len(p.disabledKeys()) > 0
	case appJSPath:
		return inject.RewritesAppJS(&p.cfg.UI)
	case manifestPath:
		return p.cfg.UI.Favicon.Size192x192 != "" || p.cfg.UI.Favicon.Size512x512 != ""
	}
	ext := path.Ext(urlPath)
	return ext == "" || ext == ".html"
}

func (p *Proxy) modifyResponse(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK || resp.Request.Method == http.MethodHead {
		return nil
	}
	urlPath := resp.Request.URL.Path
	scope := statuspage.FromContext(resp.Request.Context())
	isHTML := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html")
	disabled := p.disabledKeys()
	isStatuses := urlPath == statuspage.StatusesPath && (scope != nil || len(disabled) > 0)
	if !isHTML && !isStatuses && !(p.mayRewrite(resp.Request) && (urlPath == appJSPath || urlPath == manifestPath)) {
		return nil
	}
	if enc := strings.ToLower(resp.Header.Get("Content-Encoding")); enc != "" && enc != "identity" && enc != "gzip" {
		if isStatuses && scope != nil {
			return fmt.Errorf("cannot filter the endpoint statuses for %s: content-encoding %s", scopeName(scope), enc)
		}
		slog.Warn("cannot patch upstream response, passing it through", "path", urlPath, "content-encoding", enc)
		return nil
	}
	body, err := readBody(resp)
	if err != nil {
		return fmt.Errorf("reading upstream response %s: %w", urlPath, err)
	}
	cfg := p.cfg
	if scope != nil && scope.Strict {
		// [baseurl] is the domain of the status page there.
		cfg = cfg.Clone()
		cfg.BaseURL = scope.Origin
	}
	switch {
	case isStatuses:
		filtered, err := statuspage.FilterStatuses(body, func(key string) bool {
			return !disabled[key] && (scope == nil || scope.Visible(key))
		})
		switch {
		case err == nil:
			body = filtered
		case scope != nil:
			return fmt.Errorf("filtering the endpoint statuses for %s: %w", scopeName(scope), err)
		default:
			slog.Warn("cannot leave the disabled endpoints out of the statuses", "error", err)
		}
	case isHTML:
		var report inject.Report
		body, report = inject.HTML(body, cfg)
		p.tracker.Record("html", report)
		if scope != nil && scope.Page != nil {
			body, report = inject.StatusPage(body, scope.Page, !scope.Strict)
			p.tracker.Record("html (status page)", report)
		}
	case urlPath == appJSPath:
		var report inject.Report
		body, report = inject.AppJS(body, &p.cfg.UI)
		p.tracker.Record("app.js", report)
	case urlPath == manifestPath:
		patched, err := inject.Manifest(body, cfg)
		p.tracker.Record("manifest", single("icons", err == nil))
		if err == nil {
			body = patched
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("ETag")
	resp.Header.Del("Last-Modified")
	resp.Header.Set("Cache-Control", "no-cache")
	return nil
}

func scopeName(s *statuspage.Scope) string {
	if s.Page == nil {
		return "the main site"
	}
	return fmt.Sprintf("status page %q", s.Page.Slug)
}

func single(name string, ok bool) inject.Report {
	if ok {
		return inject.Report{Applied: []string{name}}
	}
	return inject.Report{Missing: []string{name}}
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	var reader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBodySize {
		return nil, fmt.Errorf("response larger than %d bytes", maxBodySize)
	}
	return data, nil
}
