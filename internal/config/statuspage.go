package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// StatusPage shows only the endpoints of some groups and single endpoints, at /status/{slug} and as the whole
// site on its domains. With a password it is private: its endpoints are hidden everywhere else.
type StatusPage struct {
	ID      string   `json:"id"`
	Slug    string   `json:"slug"`
	Domains []string `json:"domains,omitempty"`
	Groups  []string `json:"groups"`
	// Endpoints are the keys of single endpoints, shown in addition to the groups.
	Endpoints      []string `json:"endpoints,omitempty"`
	Title          string   `json:"title,omitempty"`
	Heading        string   `json:"heading,omitempty"`
	Subheading     string   `json:"subheading,omitempty"`
	PasswordBcrypt string   `json:"passwordBcrypt,omitempty"`
}

// Private reports whether visitors need the password of the page.
func (p StatusPage) Private() bool { return p.PasswordBcrypt != "" }

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
)

func (p StatusPage) clone() StatusPage {
	p.Domains = append([]string(nil), p.Domains...)
	p.Groups = append([]string(nil), p.Groups...)
	p.Endpoints = append([]string(nil), p.Endpoints...)
	return p
}

// CloneStatusPages returns a copy that shares no slices with pages.
func CloneStatusPages(pages []StatusPage) []StatusPage {
	if pages == nil {
		return nil
	}
	out := make([]StatusPage, len(pages))
	for i, p := range pages {
		out[i] = p.clone()
	}
	return out
}

// NormalizeDomain lowercases a domain and removes a scheme, path, port and trailing dot, so it can be compared with a Host header.
func NormalizeDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	return strings.TrimSuffix(s, ".")
}

// Normalize trims the fields, lowercases the slug and endpoint keys, normalizes the domains, and removes empty and
// duplicate domains, groups and endpoints.
func (p *StatusPage) Normalize() {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Title, p.Heading, p.Subheading = strings.TrimSpace(p.Title), strings.TrimSpace(p.Heading), strings.TrimSpace(p.Subheading)
	p.Domains = uniqueValues(p.Domains, NormalizeDomain)
	p.Groups = uniqueValues(p.Groups, strings.TrimSpace)
	p.Endpoints = uniqueValues(p.Endpoints, func(s string) string { return strings.ToLower(strings.TrimSpace(s)) })
}

// EndpointKey mirrors gatus/config/key.ConvertGroupAndNameToKey.
func EndpointKey(group, name string) string {
	return sanitizeKey(group) + "_" + sanitizeKey(name)
}

func sanitizeKey(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.NewReplacer("/", "-", "_", "-", ".", "-", ",", "-", " ", "-", "#", "-", "+", "-", "&", "-").Replace(s)
}

// Shows reports whether the endpoint with the key is on the page: as a single endpoint, or through its group.
// Sanitizing turns "_" into "-", so the group of a key is the part before the first "_".
func (p *StatusPage) Shows(key string) bool {
	key = strings.ToLower(key)
	if p.HasEndpoint(key) {
		return true
	}
	group, _, ok := strings.Cut(key, "_")
	if !ok {
		return false
	}
	for _, g := range p.Groups {
		if sanitizeKey(g) == group {
			return true
		}
	}
	return false
}

// HiddenKey reports whether the endpoint belongs to a private status page, so it must not be shown outside it.
// current is the page the visitor is on (nil for the main site); a private page that shows the key, and that the
// visitor has logged in to, may show it.
func (cfg *Config) HiddenKey(key string, current *StatusPage) bool {
	if current != nil && current.Private() && current.Shows(key) {
		return false
	}
	for i := range cfg.StatusPages {
		if p := &cfg.StatusPages[i]; p.Private() && p.Shows(key) {
			return true
		}
	}
	return false
}

// HasPrivateStatusPages reports whether some endpoints may be hidden.
func (cfg *Config) HasPrivateStatusPages() bool {
	for i := range cfg.StatusPages {
		if cfg.StatusPages[i].Private() {
			return true
		}
	}
	return false
}

// HasEndpoint reports whether the page shows the endpoint with the key as a single endpoint.
func (p *StatusPage) HasEndpoint(key string) bool {
	for _, k := range p.Endpoints {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

func uniqueValues(values []string, clean func(string) string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		v = clean(v)
		if v == "" || seen[strings.ToLower(v)] {
			continue
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	return out
}

// HasGroup reports whether the page shows the endpoints of the group.
func (p *StatusPage) HasGroup(group string) bool {
	for _, g := range p.Groups {
		if strings.EqualFold(g, strings.TrimSpace(group)) {
			return true
		}
	}
	return false
}

func (cfg *Config) validateStatusPages() error {
	baseHost := ""
	if u, err := url.Parse(cfg.BaseURL); err == nil {
		baseHost = strings.ToLower(u.Hostname())
	}
	slugs, domains := map[string]bool{}, map[string]string{}
	for i := range cfg.StatusPages {
		p := &cfg.StatusPages[i]
		p.Normalize()
		if !slugPattern.MatchString(p.Slug) {
			return fmt.Errorf("status page %q: the slug may only contain lowercase letters, digits and dashes", p.Slug)
		}
		if slugs[p.Slug] {
			return fmt.Errorf("there is already a status page with the slug %q", p.Slug)
		}
		slugs[p.Slug] = true
		if len(p.Groups) == 0 && len(p.Endpoints) == 0 {
			return fmt.Errorf("status page %q: choose at least one group or endpoint", p.Slug)
		}
		for _, d := range p.Domains {
			if !domainPattern.MatchString(d) {
				return fmt.Errorf("status page %q: %q is not a domain name (no scheme, port or path, e.g. status.example.com)", p.Slug, d)
			}
			if d == baseHost {
				return fmt.Errorf("status page %q: %s is the domain of the base URL", p.Slug, d)
			}
			if other, ok := domains[d]; ok {
				return fmt.Errorf("the domain %s is used by the status pages %q and %q", d, other, p.Slug)
			}
			domains[d] = p.Slug
		}
	}
	return nil
}

// StatusPageByHost returns the status page mapped to the host of a request (the port is ignored).
func (cfg *Config) StatusPageByHost(host string) *StatusPage {
	if len(cfg.StatusPages) == 0 {
		return nil
	}
	host = NormalizeDomain(host)
	for i := range cfg.StatusPages {
		for _, d := range cfg.StatusPages[i].Domains {
			if d == host {
				return &cfg.StatusPages[i]
			}
		}
	}
	return nil
}

// StatusPageBySlug returns the status page with the slug.
func (cfg *Config) StatusPageBySlug(slug string) *StatusPage {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return nil
	}
	for i := range cfg.StatusPages {
		if cfg.StatusPages[i].Slug == slug {
			return &cfg.StatusPages[i]
		}
	}
	return nil
}
