package config

import (
	"strings"
	"testing"
)

func TestStatusPages_Validate(t *testing.T) {
	cases := map[string]struct {
		pages []StatusPage
		err   string
	}{
		"valid":            {[]StatusPage{{Slug: "a", Groups: []string{"core"}, Domains: []string{"status.a.nl", "status.localhost"}}}, ""},
		"no domains":       {[]StatusPage{{Slug: "a", Groups: []string{"core"}}}, ""},
		"bad slug":         {[]StatusPage{{Slug: "a b", Groups: []string{"core"}}}, "slug"},
		"duplicate slug":   {[]StatusPage{{Slug: "a", Groups: []string{"x"}}, {Slug: "A", Groups: []string{"y"}}}, "already"},
		"no groups":        {[]StatusPage{{Slug: "a", Groups: []string{" "}}}, "at least one group or endpoint"},
		"only endpoints":   {[]StatusPage{{Slug: "a", Endpoints: []string{"core_api"}}}, ""},
		"bad domain":       {[]StatusPage{{Slug: "a", Groups: []string{"x"}, Domains: []string{"exa mple.com"}}}, "not a domain"},
		"base url domain":  {[]StatusPage{{Slug: "a", Groups: []string{"x"}, Domains: []string{"main.example.com"}}}, "base URL"},
		"duplicate domain": {[]StatusPage{{Slug: "a", Groups: []string{"x"}, Domains: []string{"s.nl"}}, {Slug: "b", Groups: []string{"y"}, Domains: []string{"S.nl"}}}, "used by"},
	}
	for name, c := range cases {
		cfg := &Config{BaseURL: "https://main.example.com", StatusPages: c.pages}
		err := cfg.Validate()
		if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: got %v, want %q", name, err, c.err)
		}
	}
}

func TestStatusPages_Lookup(t *testing.T) {
	cfg := &Config{StatusPages: []StatusPage{{Slug: "a", Groups: []string{"Core"}, Domains: []string{"status.a.nl"}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"status.a.nl", "STATUS.A.NL:8443", "status.a.nl."} {
		if cfg.StatusPageByHost(host) == nil {
			t.Errorf("%s should match", host)
		}
	}
	if cfg.StatusPageByHost("a.nl") != nil || cfg.StatusPageBySlug("b") != nil || cfg.StatusPageBySlug("A") == nil {
		t.Error("unexpected lookup result")
	}
	if !cfg.StatusPages[0].HasGroup("core") || cfg.StatusPages[0].HasGroup("web") {
		t.Error("HasGroup should compare case-insensitively")
	}
	clone := cfg.Clone()
	clone.StatusPages[0].Groups[0] = "changed"
	if cfg.StatusPages[0].Groups[0] != "Core" {
		t.Error("Clone should copy the status pages")
	}
}

func TestStatusPages_Visibility(t *testing.T) {
	cfg := &Config{StatusPages: []StatusPage{
		{Slug: "private", Groups: []string{"Core Services"}, Endpoints: []string{" Web_Shop "}, PasswordBcrypt: "x"},
		{Slug: "public", Groups: []string{"web"}},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	private, public := &cfg.StatusPages[0], &cfg.StatusPages[1]
	for key, want := range map[string]bool{"core-services_a": true, "web_shop": true, "WEB_SHOP": true, "web_other": false, "core_a": false, "nokey": false} {
		if got := private.Shows(key); got != want {
			t.Errorf("Shows(%q) = %v, want %v", key, got, want)
		}
	}
	cases := []struct {
		key     string
		current *StatusPage
		hidden  bool
	}{
		{"core-services_a", nil, true},
		{"web_shop", nil, true},
		{"web_other", nil, false},
		{"web_shop", public, true},
		{"web_shop", private, false},
		{"web_other", private, false},
	}
	for _, c := range cases {
		if got := cfg.HiddenKey(c.key, c.current); got != c.hidden {
			t.Errorf("HiddenKey(%q, %v) = %v, want %v", c.key, c.current != nil, got, c.hidden)
		}
	}
	if !cfg.HasPrivateStatusPages() || (&Config{StatusPages: []StatusPage{*public}}).HasPrivateStatusPages() {
		t.Error("HasPrivateStatusPages is wrong")
	}
}
