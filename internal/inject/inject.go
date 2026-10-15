// Package inject rewrites the HTML, JavaScript and manifest served by Gatus.
package inject

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"path"
	"regexp"
	"strings"

	"gatusmod/internal/config"
)

// Report lists which patches were applied, so a Gatus upgrade that breaks a patch is visible.
type Report struct {
	Applied []string `json:"applied"`
	Missing []string `json:"missing"`
}

func (r *Report) ok(name string)   { r.Applied = append(r.Applied, name) }
func (r *Report) miss(name string) { r.Missing = append(r.Missing, name) }

type faviconTag struct {
	name    string
	pattern *regexp.Regexp
	build   func(href string) string
	value   func(f *config.Favicon) string
}

func linkPattern(rel, sizes string) *regexp.Regexp {
	p := `(?i)<link\b[^>]*\brel="` + regexp.QuoteMeta(rel) + `"[^>]*>`
	if sizes != "" {
		p = `(?i)<link\b[^>]*\brel="` + regexp.QuoteMeta(rel) + `"[^>]*\bsizes="` + sizes + `"[^>]*>`
	}
	return regexp.MustCompile(p)
}

func iconLink(rel, sizes string) func(string) string {
	return func(href string) string {
		attrs := `rel="` + rel + `"`
		if t := imageType(href); t != "" {
			attrs += ` type="` + t + `"`
		}
		if sizes != "" {
			attrs += ` sizes="` + sizes + `"`
		}
		return `<link ` + attrs + ` href="` + html.EscapeString(href) + `" />`
	}
}

var faviconTags = []faviconTag{
	{"favicon.default", linkPattern("shortcut icon", ""), iconLink("shortcut icon", ""), func(f *config.Favicon) string { return f.Default }},
	{"favicon.size16x16", linkPattern("icon", "16x16"), iconLink("icon", "16x16"), func(f *config.Favicon) string { return f.Size16x16 }},
	{"favicon.size32x32", linkPattern("icon", "32x32"), iconLink("icon", "32x32"), func(f *config.Favicon) string { return f.Size32x32 }},
	{"favicon.size192x192", linkPattern("icon", "192x192"), iconLink("icon", "192x192"), func(f *config.Favicon) string { return f.Size192x192 }},
	{"favicon.size512x512", linkPattern("icon", "512x512"), iconLink("icon", "512x512"), func(f *config.Favicon) string { return f.Size512x512 }},
	{"favicon.apple-touch", linkPattern("apple-touch-icon", ""), iconLink("apple-touch-icon", "180x180"), func(f *config.Favicon) string { return f.AppleTouch }},
}

func imageType(href string) string {
	ext := strings.ToLower(path.Ext(strings.SplitN(href, "?", 2)[0]))
	switch ext {
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	}
	return ""
}

// HTML patches the Gatus index page: favicons are replaced (or added), and the GatusMOD assets, analytics
// and footer are injected.
func HTML(body []byte, cfg *config.Config) ([]byte, Report) {
	var report Report
	s := string(body)
	var head strings.Builder
	head.WriteString("<!-- GatusMOD -->\n")
	for _, tag := range faviconTags {
		href := tag.value(&cfg.UI.Favicon)
		if href == "" {
			continue
		}
		newTag := tag.build(cfg.ResolveURL(href))
		if loc := tag.pattern.FindStringIndex(s); loc != nil {
			s = s[:loc[0]] + newTag + s[loc[1]:]
		} else {
			head.WriteString(newTag + "\n")
		}
		report.ok(tag.name)
	}
	settings, _ := json.Marshal(map[string]any{
		"headingShow":  cfg.UI.HeadingVisible(),
		"subtitleShow": cfg.UI.SubtitleVisible(),
		"timeFormat":   cfg.UI.TimeFormat,
		"dateFormat":   cfg.UI.DateFormat,
	})
	head.WriteString(`<link rel="stylesheet" href="/mod/static/mod.css" />` + "\n")
	head.WriteString(`<script>window.gatusmod=` + string(settings) + `;</script>` + "\n")
	if cfg.UI.TimeFormat != "" || cfg.UI.DateFormat != "" {
		// Not deferred, so the Date formatting is changed before the deferred Gatus scripts run.
		head.WriteString(`<script src="/mod/static/datetime.js"></script>` + "\n")
	}
	head.WriteString(`<script defer src="/mod/static/mod.js"></script>` + "\n")
	if a := cfg.Analytics; a != nil {
		src, id := html.EscapeString(cfg.ResolveURL(a.ScriptURL)), html.EscapeString(a.SiteID)
		switch a.Provider {
		case "umami":
			head.WriteString(`<script defer src="` + src + `" data-website-id="` + id + `"></script>` + "\n")
		case "plausible":
			head.WriteString(`<script defer data-domain="` + id + `" src="` + src + `"></script>` + "\n")
		}
		report.ok("analytics." + a.Provider)
	}
	if i := strings.Index(strings.ToLower(s), "</head>"); i >= 0 {
		s = s[:i] + head.String() + s[i:]
		report.ok("head")
	} else {
		report.miss("head")
	}
	if cfg.UI.Footer != "" {
		footer := `<footer id="gatusmod-footer" class="gatusmod-footer">` + cfg.ResolveURL(cfg.UI.Footer) + "</footer>\n"
		if i := strings.LastIndex(strings.ToLower(s), "</body>"); i >= 0 {
			s = s[:i] + footer + s[i:]
			report.ok("footer")
		} else {
			report.miss("footer")
		}
	}
	return []byte(s), report
}

var (
	titleTag  = regexp.MustCompile(`(?is)<title>.*?</title>`)
	titleMeta = regexp.MustCompile(`(?i)(<meta\b[^>]*\bname="(?:apple-mobile-web-app-title|application-name)"[^>]*\bcontent=")[^"]*(")`)
)

// StatusPage brands the Gatus index page of a status page. With scopeScript (the status page on the main site)
// statuspage.js is added, which keeps the slug in the URL and on the API requests of the dashboard.
func StatusPage(body []byte, page *config.StatusPage, scopeScript bool) ([]byte, Report) {
	var report Report
	s := string(body)
	var head strings.Builder
	if page.Title != "" {
		if titleTag.MatchString(s) {
			s = titleTag.ReplaceAllLiteralString(s, "<title>"+html.EscapeString(page.Title)+"</title>")
			s = titleMeta.ReplaceAllString(s, "${1}"+strings.ReplaceAll(html.EscapeString(page.Title), "$", "$$")+"${2}")
			report.ok("statuspage.title")
		} else {
			report.miss("statuspage.title")
		}
	}
	override := map[string]string{}
	if page.Heading != "" {
		override["dashboardHeading"] = page.Heading
	}
	if page.Subheading != "" {
		override["dashboardSubheading"] = page.Subheading
	}
	if len(override) > 0 {
		if strings.Contains(s, "window.config") {
			data, _ := json.Marshal(override)
			head.WriteString(`<script>window.config=Object.assign(window.config||{},` + string(data) + `);</script>` + "\n")
			report.ok("statuspage.config")
		} else {
			report.miss("statuspage.config")
		}
	}
	if scopeScript {
		slug, _ := json.Marshal(page.Slug)
		head.WriteString(`<script>window.gatusmodStatusPage=` + string(slug) + `;</script>` + "\n")
		head.WriteString(`<script src="/mod/static/statuspage.js"></script>` + "\n")
	}
	if head.Len() > 0 {
		if i := strings.Index(strings.ToLower(s), "</head>"); i >= 0 {
			s = s[:i] + head.String() + s[i:]
			report.ok("statuspage.head")
		} else {
			report.miss("statuspage.head")
		}
	}
	if page.Private() {
		logout := `<form method="post" action="/mod/status-logout" class="gatusmod-status-logout">`
		if scopeScript {
			logout += `<input type="hidden" name="page" value="` + html.EscapeString(page.Slug) + `" />`
		}
		logout += `<button type="submit">Log out</button></form>` + "\n"
		if i := strings.LastIndex(strings.ToLower(s), "</body>"); i >= 0 {
			s = s[:i] + logout + s[i:]
			report.ok("statuspage.logout")
		} else {
			report.miss("statuspage.logout")
		}
	}
	return []byte(s), report
}

// homeItemsPerPage matches the minified `const itemsPerPage = 96` of the Home view in Gatus' app.js,
// e.g. `Pa=96,Ka=50;var Va={__name:"Home"`.
var homeItemsPerPage = regexp.MustCompile(`(\b[A-Za-z_$][\w$]*=)96(,[A-Za-z_$][\w$]*=\d+;var [A-Za-z_$][\w$]*=\{__name:"Home")`)

// chartHourFormat is the date-fns format of the hourly x-axis of the response time chart, e.g. "Oct 14, 4PM".
var chartHourFormat = []byte(`displayFormats:{hour:"MMM d, ha"`)

// AppJS patches Gatus' app.js: the number of endpoints per dashboard page and the 24-hour chart axis.
func AppJS(body []byte, ui *config.UI) ([]byte, Report) {
	var report Report
	if ui.ItemsPerPage > 0 {
		if loc := homeItemsPerPage.FindSubmatchIndex(body); loc != nil {
			var out bytes.Buffer
			out.Write(body[:loc[3]])
			fmt.Fprintf(&out, "%d", ui.ItemsPerPage)
			out.Write(body[loc[4]:])
			body = out.Bytes()
			report.ok("items-per-page")
		} else {
			report.miss("items-per-page")
		}
	}
	if ui.TimeFormat == config.TimeFormat24h {
		if bytes.Contains(body, chartHourFormat) {
			body = bytes.ReplaceAll(body, chartHourFormat, []byte(`displayFormats:{hour:"MMM d, HH:mm"`))
			report.ok("chart-time-format")
		} else {
			report.miss("chart-time-format")
		}
	}
	return body, report
}

// RewritesAppJS reports whether AppJS changes anything for these settings.
func RewritesAppJS(ui *config.UI) bool {
	return ui.ItemsPerPage > 0 || ui.TimeFormat == config.TimeFormat24h
}

// Manifest replaces or adds the 192x192 and 512x512 icons of the web app manifest.
func Manifest(body []byte, cfg *config.Config) ([]byte, error) {
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		return body, err
	}
	icons, _ := manifest["icons"].([]any)
	for _, entry := range [][2]string{{"192x192", cfg.UI.Favicon.Size192x192}, {"512x512", cfg.UI.Favicon.Size512x512}} {
		sizes, href := entry[0], entry[1]
		if href == "" {
			continue
		}
		icon := map[string]any{"src": cfg.ResolveURL(href), "sizes": sizes}
		if t := imageType(href); t != "" {
			icon["type"] = t
		}
		replaced := false
		for i, existing := range icons {
			if m, ok := existing.(map[string]any); ok && m["sizes"] == sizes {
				icons[i], replaced = icon, true
			}
		}
		if !replaced {
			icons = append(icons, icon)
		}
	}
	manifest["icons"] = icons
	return json.MarshalIndent(manifest, "", "  ")
}
