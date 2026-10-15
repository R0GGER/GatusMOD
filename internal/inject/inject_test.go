package inject

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gatusmod/internal/config"
)

// Rendered output of gatus/web/app/public/index.html (head trimmed to the relevant tags).
const gatusIndex = `<!DOCTYPE html><html lang="en" class="dark"><head><meta charset="utf-8" />
<title>Status</title>
<link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon.png" />
<link rel="icon" type="image/png" sizes="32x32" href="/favicon-32x32.png" />
<link rel="icon" type="image/png" sizes="16x16" href="/favicon-16x16.png" />
<link rel="manifest" href="/manifest.json" crossorigin="use-credentials" />
<link rel="shortcut icon" href="/favicon.ico" />
<link rel="stylesheet" href="/css/custom.css" />
</head><body><div id="app"></div></body></html>`

func testConfig() *config.Config {
	f := false
	return &config.Config{
		BaseURL: "https://status.example.com/",
		UI: config.UI{
			DashboardHeadingShow: &f,
			Footer:               `<p>&copy; Example <a href="[baseurl]/about">about</a></p>`,
			Favicon: config.Favicon{
				Default:     "[baseurl]/images/favicon.ico",
				Size16x16:   "[baseurl]/images/favicon_16.png",
				AppleTouch:  "[baseurl]/favicon_256.png",
				Size192x192: "[baseurl]/favicon_256.png",
				Size512x512: "[baseurl]/images/favicon_512.png",
			},
		},
		Analytics: &config.Analytics{Provider: "umami", ScriptURL: "https://umami.example.com/script.js", SiteID: "abc"},
	}
}

func TestHTML(t *testing.T) {
	out, report := HTML([]byte(gatusIndex), testConfig())
	s := string(out)
	for _, want := range []string{
		`<link rel="shortcut icon" type="image/x-icon" href="https://status.example.com/images/favicon.ico" />`,
		`<link rel="icon" type="image/png" sizes="16x16" href="https://status.example.com/images/favicon_16.png" />`,
		`<link rel="icon" type="image/png" sizes="32x32" href="/favicon-32x32.png" />`,
		`<link rel="apple-touch-icon" type="image/png" sizes="180x180" href="https://status.example.com/favicon_256.png" />`,
		`sizes="192x192" href="https://status.example.com/favicon_256.png"`,
		`sizes="512x512" href="https://status.example.com/images/favicon_512.png"`,
		`window.gatusmod={"dateFormat":"","headingShow":false,"subtitleShow":true,"timeFormat":""}`,
		`<script defer src="/mod/static/mod.js"></script>`,
		`data-website-id="abc"`,
		`<footer id="gatusmod-footer" class="gatusmod-footer"><p>&copy; Example <a href="https://status.example.com/about">about</a></p></footer>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("expected output to contain %s\n%s", want, s)
		}
	}
	for _, gone := range []string{`href="/favicon.ico"`, `href="/favicon-16x16.png"`, `href="/apple-touch-icon.png"`} {
		if strings.Contains(s, gone) {
			t.Errorf("expected %s to be replaced", gone)
		}
	}
	if len(report.Missing) != 0 {
		t.Errorf("unexpected missing patches %v", report.Missing)
	}
	if strings.Index(s, "<footer") > strings.Index(s, "</body>") || strings.Index(s, "mod.js") > strings.Index(s, "</head>") {
		t.Error("injected content is in the wrong place")
	}
}

func TestHTML_Plausible(t *testing.T) {
	cfg := &config.Config{Analytics: &config.Analytics{Provider: "plausible", ScriptURL: "https://p.example.com/js/script.js", SiteID: "status.example.com"}}
	out, _ := HTML([]byte(gatusIndex), cfg)
	if !strings.Contains(string(out), `<script defer data-domain="status.example.com" src="https://p.example.com/js/script.js"></script>`) {
		t.Error("expected plausible script tag")
	}
	if strings.Contains(string(out), "gatusmod-footer") {
		t.Error("no footer expected when none is configured")
	}
}

func TestHTML_DateTime(t *testing.T) {
	out, _ := HTML([]byte(gatusIndex), &config.Config{})
	if strings.Contains(string(out), "datetime.js") {
		t.Error("datetime.js is only needed when a format is configured")
	}
	out, _ = HTML([]byte(gatusIndex), &config.Config{UI: config.UI{TimeFormat: "24h", DateFormat: "DD/MM/YYYY"}})
	s := string(out)
	for _, want := range []string{`"timeFormat":"24h"`, `"dateFormat":"DD/MM/YYYY"`, `<script src="/mod/static/datetime.js"></script>`} {
		if !strings.Contains(s, want) {
			t.Errorf("expected output to contain %s\n%s", want, s)
		}
	}
}

func TestAppJS(t *testing.T) {
	js := []byte(`Oa={key:3,class:"mt-12 pb-8"},Pa=96,Ka=50;var Va={__name:"Home",props:{}}`)
	out, report := AppJS(js, &config.UI{ItemsPerPage: 24})
	if len(report.Missing) != 0 || string(out) != `Oa={key:3,class:"mt-12 pb-8"},Pa=24,Ka=50;var Va={__name:"Home",props:{}}` {
		t.Errorf("unexpected patch result %v %s", report, out)
	}
	if _, report := AppJS([]byte(`var x=96;`), &config.UI{ItemsPerPage: 24}); len(report.Missing) != 1 {
		t.Error("expected no match")
	}
}

func TestAppJS_ChartTimeFormat(t *testing.T) {
	js := []byte(`time:{unit:"hour",displayFormats:{hour:"MMM d, ha",day:"MMM d"}}`)
	out, report := AppJS(js, &config.UI{TimeFormat: "24h"})
	if len(report.Applied) != 1 || string(out) != `time:{unit:"hour",displayFormats:{hour:"MMM d, HH:mm",day:"MMM d"}}` {
		t.Errorf("unexpected patch result %v %s", report, out)
	}
	if out, _ := AppJS(js, &config.UI{TimeFormat: "12h"}); string(out) != string(js) {
		t.Error("the 12-hour chart axis of Gatus must be kept")
	}
}

// TestAppJS_RealBundle checks the patterns against the app.js shipped in the Gatus checkout next to this project.
func TestAppJS_RealBundle(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "gatus", "web", "static", "js", "app.js"))
	if err != nil {
		t.Skip("gatus checkout not found:", err)
	}
	if _, report := AppJS(data, &config.UI{ItemsPerPage: 24, TimeFormat: "24h"}); len(report.Missing) != 0 {
		t.Errorf("patterns not found in the Gatus bundle: %v", report.Missing)
	}
}

func TestManifest(t *testing.T) {
	in := `{"name":"Gatus","icons":[{"src":"/logo-192x192.png","sizes":"192x192","type":"image/png"},{"src":"/logo-512x512.png","sizes":"512x512","type":"image/png"}]}`
	out, err := Manifest([]byte(in), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Name  string              `json:"name"`
		Icons []map[string]string `json:"icons"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "Gatus" || len(m.Icons) != 2 || m.Icons[0]["src"] != "https://status.example.com/favicon_256.png" || m.Icons[1]["src"] != "https://status.example.com/images/favicon_512.png" {
		t.Errorf("unexpected manifest %s", out)
	}
}
