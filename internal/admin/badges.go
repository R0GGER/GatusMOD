package admin

import (
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"

	"gatusmod/internal/badge"
)

var chartDurations = map[string]bool{"24h": true, "7d": true, "30d": true}

type badgeEndpoint struct {
	Name, Group, Key, Page string
	Badges                 []badgeCode
}

type badgeCode struct {
	Label, URL, Markdown, HTML string
}

type option struct {
	Value, Label string
}

// embed holds the choices of the "Embed badge" generator.
type embed struct {
	Duration, Style, Content, Check, Snippet string
	URL, Code                                string
	Checks                                   []option
}

var (
	styleOptions   = []option{{"flat", "Flat"}, {"flat-square", "Flat square"}, {"plastic", "Plastic"}}
	contentOptions = []option{{badge.ContentPercentage, "Percentage"}, {badge.ContentEndpoints, "Percentage and endpoints"}}
	snippetOptions = []option{{"markdown", "Markdown"}, {"html", "HTML"}, {"url", "URL"}}
	durationLabels = map[string]string{"1h": "1 hour", "24h": "24 hours", "7d": "7 days", "30d": "30 days"}
)

func pick(value string, options []option) string {
	for _, o := range options {
		if o.Value == value {
			return value
		}
	}
	return options[0].Value
}

func (a *Admin) badges(w http.ResponseWriter, r *http.Request) {
	cfg := a.deps.Config()
	q := r.URL.Query()
	duration := q.Get("duration")
	if !contains(badge.Durations, duration) {
		duration = "24h"
	}
	endpoints, source, err := badge.List(r.Context(), a.deps.Client, cfg, a.deps.Syncer.Records())
	base := badge.PublicBaseURL(cfg, r)

	durations := make([]option, 0, len(badge.Durations))
	for _, d := range badge.Durations {
		durations = append(durations, option{d, durationLabels[d]})
	}
	checks := []option{{badge.CheckOverall, "Overall"}}
	groups := map[string]bool{}
	for _, ep := range endpoints {
		if ep.Group != "" && !groups[ep.Group] {
			groups[ep.Group] = true
		}
	}
	groupNames := make([]string, 0, len(groups))
	for g := range groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)
	for _, g := range groupNames {
		checks = append(checks, option{"group:" + g, "Group: " + g})
	}
	for _, imp := range cfg.Imports {
		kind := "Database"
		if imp.Local() {
			kind = "Table"
		}
		checks = append(checks, option{"import:" + imp.ID, kind + ": " + imp.Label()})
	}
	e := embed{
		Duration: pick(q.Get("embedDuration"), append([]option{{"30d", ""}}, durations...)),
		Style:    pick(q.Get("style"), styleOptions),
		Content:  pick(q.Get("content"), contentOptions),
		Check:    pick(q.Get("check"), checks),
		Snippet:  pick(q.Get("snippet"), snippetOptions),
		Checks:   checks,
	}
	e.URL = badge.URL(base, e.Duration, e.Style, e.Content, e.Check)
	link := base + "/"
	alt := "Uptime"
	switch e.Snippet {
	case "markdown":
		e.Code = fmt.Sprintf("[![%s](%s)](%s)", alt, e.URL, link)
	case "html":
		e.Code = fmt.Sprintf(`<a href="%s"><img src="%s" alt="%s"></a>`, html.EscapeString(link), html.EscapeString(e.URL), alt)
	default:
		e.Code = e.URL
	}

	badgeStyle := pick(q.Get("badgeStyle"), styleOptions)
	list := make([]badgeEndpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		be := badgeEndpoint{Name: ep.Name, Group: ep.Group, Key: ep.Key, Page: base + "/endpoints/" + ep.Key}
		urls := [][2]string{
			{"Health", badge.EndpointURL(base, ep.Key, badge.KindHealth, "", badgeStyle)},
			{"Uptime " + duration, badge.EndpointURL(base, ep.Key, badge.KindUptime, duration, badgeStyle)},
			{"Response time " + duration, badge.EndpointURL(base, ep.Key, badge.KindResponseTimes, duration, badgeStyle)},
		}
		if chartDurations[duration] {
			urls = append(urls, [2]string{"Response time chart " + duration, base + "/api/v1/endpoints/" + ep.Key + "/response-times/" + duration + "/chart.svg"})
		}
		for _, u := range urls {
			alt := ep.Name + " " + strings.ToLower(u[0])
			be.Badges = append(be.Badges, badgeCode{
				Label:    u[0],
				URL:      u[1],
				Markdown: fmt.Sprintf("[![%s](%s)](%s)", alt, u[1], be.Page),
				HTML:     fmt.Sprintf(`<a href="%s"><img src="%s" alt="%s"></a>`, html.EscapeString(be.Page), html.EscapeString(u[1]), html.EscapeString(alt)),
			})
		}
		list = append(list, be)
	}
	data := map[string]any{
		"Config":     cfg,
		"Endpoints":  list,
		"Duration":   duration,
		"BadgeStyle": badgeStyle,
		"Source":     source,
		"Embed":      e,
		"Periods":    durations,
		"StyleOpts":  styleOptions,
		"Contents":   contentOptions,
		"Snippets":   snippetOptions,
	}
	if err != nil {
		data["Error"] = err.Error()
	}
	a.render(w, r, "badges.html", data)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
