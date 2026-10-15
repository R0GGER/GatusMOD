package inject

import (
	"strings"
	"testing"

	"gatusmod/internal/config"
)

// Head of the index page of Gatus v5.37.
const gatusIndexHead = `<!doctype html><html><head><meta charset="utf-8"/><script>window.config = {logo: "", header: "Gatus", dashboardHeading: "Health Dashboard", dashboardSubheading: "Monitor"};</script>` +
	`<title>Health Dashboard | Gatus</title><meta name="apple-mobile-web-app-title" content="Health Dashboard | Gatus"/><meta name="application-name" content="Health Dashboard | Gatus"/>` +
	`<script defer="defer" src="/js/app.js"></script></head><body><div id="app"></div></body></html>`

func TestStatusPage(t *testing.T) {
	page := &config.StatusPage{Slug: "klant-a", Title: "Klant <A> $1", Heading: "Klant A", Subheading: "Status van </script>"}
	out, report := StatusPage([]byte(gatusIndexHead), page, true)
	s := string(out)
	for _, want := range []string{
		`<title>Klant &lt;A&gt; $1</title>`,
		`name="apple-mobile-web-app-title" content="Klant &lt;A&gt; $1"`,
		`name="application-name" content="Klant &lt;A&gt; $1"`,
		`window.config=Object.assign(window.config||{},{"dashboardHeading":"Klant A","dashboardSubheading":"Status van \u003c/script\u003e"});`,
		`window.gatusmodStatusPage="klant-a";`,
		`<script src="/mod/static/statuspage.js"></script>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if strings.Index(s, "statuspage.js") > strings.Index(s, "</head>") || len(report.Missing) != 0 {
		t.Errorf("unexpected result %+v\n%s", report, s)
	}

	out, _ = StatusPage([]byte(gatusIndexHead), &config.StatusPage{Slug: "klant-a"}, false)
	if string(out) != gatusIndexHead {
		t.Errorf("a page without branding on its own domain should not change the HTML:\n%s", out)
	}
	_, report = StatusPage([]byte(`<html><head></head></html>`), page, false)
	if len(report.Missing) != 2 {
		t.Errorf("title and config should be reported missing, got %+v", report)
	}
}

func TestStatusPage_Logout(t *testing.T) {
	page := &config.StatusPage{Slug: "geheim", PasswordBcrypt: "x"}
	out, report := StatusPage([]byte(gatusIndexHead), page, true)
	s := string(out)
	if !strings.Contains(s, `<form method="post" action="/mod/status-logout" class="gatusmod-status-logout"><input type="hidden" name="page" value="geheim" />`) ||
		strings.Index(s, "status-logout") > strings.Index(s, "</body>") || len(report.Missing) != 0 {
		t.Errorf("a private page on the main site needs a logout button with its slug:\n%s", s)
	}
	out, _ = StatusPage([]byte(gatusIndexHead), page, false)
	if s := string(out); !strings.Contains(s, "status-logout") || strings.Contains(s, `name="page"`) {
		t.Errorf("on its own domain the logout button needs no slug:\n%s", s)
	}
}
