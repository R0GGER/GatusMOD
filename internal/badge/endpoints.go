// Package badge lists the Gatus endpoints and serves public badges that aggregate several endpoints.
package badge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"gatusmod/internal/config"
	"gatusmod/internal/store"
)

type Endpoint struct {
	Name, Group, Key string
	// Source is the import the endpoint came from; empty for endpoints configured in YAML (or unknown).
	Source string
}

// List asks Gatus for its endpoints. When the Gatus API is protected (security configured),
// it falls back to the endpoints generated from the imports and computes the keys the way Gatus does.
// The error explains the fallback; the list is still usable.
func List(ctx context.Context, client *http.Client, cfg *config.Config, records []store.Record) ([]Endpoint, string, error) {
	sources := make(map[string]string, len(records))
	for _, rec := range records {
		sources[Key(rec.Group, rec.Name)] = rec.Source
	}
	var statuses []struct {
		Name  string `json:"name"`
		Group string `json:"group"`
		Key   string `json:"key"`
	}
	err := func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.Upstream, "/")+"/api/v1/endpoints/statuses?page=1&pageSize=1", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(&statuses)
	}()
	if err == nil {
		out := make([]Endpoint, 0, len(statuses))
		for _, s := range statuses {
			out = append(out, Endpoint{Name: s.Name, Group: s.Group, Key: s.Key, Source: sources[s.Key]})
		}
		sortEndpoints(out)
		return out, "Gatus API", nil
	}
	out := make([]Endpoint, 0, len(records))
	for _, rec := range records {
		if rec.Name != "" {
			out = append(out, Endpoint{Name: rec.Name, Group: rec.Group, Key: Key(rec.Group, rec.Name), Source: rec.Source})
		}
	}
	sortEndpoints(out)
	return out, "imports", fmt.Errorf("the Gatus API could not be read (%v); showing the endpoints from the imports instead", err)
}

func sortEndpoints(list []Endpoint) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Group != list[j].Group {
			return list[i].Group < list[j].Group
		}
		return list[i].Name < list[j].Name
	})
}

// Key mirrors gatus/config/key.ConvertGroupAndNameToKey.
func Key(group, name string) string { return config.EndpointKey(group, name) }

// PublicBaseURL is the base URL visitors use: base-url, or the scheme and host of the request.
func PublicBaseURL(cfg *config.Config, r *http.Request) string {
	if cfg.BaseURL != "" {
		return strings.TrimRight(cfg.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	return scheme + "://" + r.Host
}
