// Package related finds endpoints that belong together, e.g. the ICMP check for the host of an HTTP endpoint.
// Gatus does not expose the endpoint type or URL, so the endpoints are read from the Gatus configuration files.
package related

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"gatusmod/internal/badge"
	"gatusmod/internal/config"
	"gatusmod/internal/store"
)

const cacheTTL = 30 * time.Second

type Endpoint struct {
	Key   string
	Group string
	Name  string
	Type  string
	Host  string
}

type Response struct {
	Type string  `json:"type"`
	Host string  `json:"host"`
	ICMP *string `json:"icmp"`
}

// endpointType maps the URL scheme to the endpoint type: http and https are both "http", and the schemes
// GatusMOD checks itself use their probe type (smtps is "smtp", postgres and redis are "database").
func endpointType(u *url.URL) string {
	s := strings.ToLower(u.Scheme)
	if t := config.SchemeType(s + "://"); t != "" {
		return t
	}
	if s == "https" {
		return "http"
	}
	return s
}

type yamlEndpoint struct {
	Name  string `yaml:"name"`
	Group string `yaml:"group"`
	URL   string `yaml:"url"`
}

// parseEndpoints reads endpoints and external endpoints. External endpoints have no URL in the file;
// urls supplies the probe URL of the ones GatusMOD generated (by key).
func parseEndpoints(data []byte, urls map[string]string) []Endpoint {
	var doc struct {
		Endpoints         []yamlEndpoint `yaml:"endpoints"`
		ExternalEndpoints []yamlEndpoint `yaml:"external-endpoints"`
	}
	if err := yaml.Unmarshal(config.ExpandEnv(data), &doc); err != nil {
		return nil
	}
	out := make([]Endpoint, 0, len(doc.Endpoints)+len(doc.ExternalEndpoints))
	add := func(e yamlEndpoint, external bool) {
		if e.Name == "" {
			return
		}
		ep := Endpoint{Key: badge.Key(e.Group, e.Name), Group: e.Group, Name: e.Name}
		raw := e.URL
		if external {
			raw = urls[ep.Key]
			ep.Type = "external"
		}
		if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && raw != "" {
			ep.Type = endpointType(u)
			ep.Host = strings.ToLower(u.Hostname())
		}
		out = append(out, ep)
	}
	for _, e := range doc.Endpoints {
		add(e, false)
	}
	for _, e := range doc.ExternalEndpoints {
		add(e, true)
	}
	return out
}

// Find returns the related endpoints of key, or false if the key is unknown.
func Find(endpoints []Endpoint, key string) (Response, bool) {
	var self *Endpoint
	for i := range endpoints {
		if endpoints[i].Key == key {
			self = &endpoints[i]
			break
		}
	}
	if self == nil {
		return Response{}, false
	}
	resp := Response{Type: self.Type, Host: self.Host}
	if self.Type == "icmp" || self.Host == "" {
		return resp, true
	}
	var candidates []Endpoint
	for _, e := range endpoints {
		if e.Type == "icmp" && e.Host == self.Host && e.Key != self.Key {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return resp, true
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		si, sj := candidates[i].Group == self.Group, candidates[j].Group == self.Group
		if si != sj {
			return si
		}
		return candidates[i].Key < candidates[j].Key
	})
	resp.ICMP = &candidates[0].Key
	return resp, true
}

// Index caches the endpoints of the Gatus configuration and reloads them when a file changes or the cache expires.
type Index struct {
	config  func() *config.Config
	records func() []store.Record

	mu        sync.Mutex
	loadedAt  time.Time
	modTime   time.Time
	path      string
	endpoints []Endpoint
}

// NewIndex reads the endpoints from the configuration files; records (may be nil) are the generated
// endpoints, which supply the URLs of external endpoints.
func NewIndex(cfg func() *config.Config, records func() []store.Record) *Index {
	return &Index{config: cfg, records: records}
}

func (ix *Index) Endpoints() []Endpoint {
	cfg := ix.config()
	files := yamlFiles(cfg)
	var latest time.Time
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.path == cfg.Path && ix.modTime.Equal(latest) && time.Since(ix.loadedAt) < cacheTTL {
		return ix.endpoints
	}
	urls := map[string]string{}
	if ix.records != nil {
		for _, r := range ix.records() {
			urls[badge.Key(r.Group, r.Name)] = r.URL
		}
	}
	var all []Endpoint
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			slog.Warn("related: cannot read configuration file", "file", f, "error", err)
			continue
		}
		all = append(all, parseEndpoints(data, urls)...)
	}
	ix.endpoints, ix.modTime, ix.loadedAt, ix.path = all, latest, time.Now(), cfg.Path
	return all
}

// yamlFiles lists every YAML file Gatus reads, including the generated endpoints file.
func yamlFiles(cfg *config.Config) []string {
	seen := map[string]bool{}
	var files []string
	add := func(p string) {
		if abs, err := filepath.Abs(p); err == nil && !seen[abs] {
			seen[abs] = true
			files = append(files, abs)
		}
	}
	if info, err := os.Stat(cfg.Path); err == nil {
		if info.IsDir() {
			_ = filepath.WalkDir(cfg.Path, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if ext := filepath.Ext(p); ext == ".yaml" || ext == ".yml" {
						add(p)
					}
				}
				return nil
			})
		} else {
			add(cfg.Path)
		}
	}
	if out, err := cfg.OutputPath(); err == nil {
		if _, err := os.Stat(out); err == nil {
			add(out)
		}
	}
	sort.Strings(files)
	return files
}

// Handler serves GET /mod/api/endpoints/{key}/related.
func (ix *Index) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mod/api/endpoints/{key}/related", func(w http.ResponseWriter, r *http.Request) {
		resp, ok := Find(ix.Endpoints(), r.PathValue("key"))
		if !ok {
			http.Error(w, "endpoint not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=30")
		_ = json.NewEncoder(w).Encode(resp)
	})
	return mux
}
