package admin

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"gatusmod/internal/config"
	"gatusmod/internal/settings"
)

// generalForm holds the values of the settings page.
type generalForm struct {
	BaseURL           string
	DefaultInterval   string
	ItemsPerPage      int
	HeadingShow       bool
	SubtitleShow      bool
	Footer            string
	TimeFormat        string
	DateFormat        string
	Logo              string
	LogoLink          string
	Favicon           config.Favicon
	AnalyticsEnabled  bool
	AnalyticsProvider string
	AnalyticsScript   string
	AnalyticsSite     string
}

func generalFromConfig(cfg *config.Config) generalForm {
	g := generalForm{
		BaseURL:         cfg.BaseURL,
		DefaultInterval: cfg.Defaults.Interval,
		ItemsPerPage:    cfg.UI.ItemsPerPage,
		HeadingShow:     cfg.UI.HeadingVisible(),
		SubtitleShow:    cfg.UI.SubtitleVisible(),
		Footer:          cfg.UI.Footer,
		TimeFormat:      cfg.UI.TimeFormat,
		DateFormat:      cfg.UI.DateFormat,
		Logo:            cfg.UI.Logo,
		LogoLink:        cfg.UI.LogoLink,
		Favicon:         cfg.UI.Favicon,
	}
	if a := cfg.Analytics; a != nil {
		g.AnalyticsEnabled, g.AnalyticsProvider, g.AnalyticsScript, g.AnalyticsSite = true, a.Provider, a.ScriptURL, a.SiteID
	}
	return g
}

func generalFromForm(r *http.Request) (generalForm, error) {
	v := func(key string) string { return strings.TrimSpace(r.PostFormValue(key)) }
	g := generalForm{
		BaseURL:         v("baseUrl"),
		DefaultInterval: v("defaultInterval"),
		HeadingShow:     r.PostFormValue("headingShow") == "on",
		SubtitleShow:    r.PostFormValue("subtitleShow") == "on",
		Footer:          strings.TrimSpace(r.PostFormValue("footer")),
		TimeFormat:      v("timeFormat"),
		DateFormat:      v("dateFormat"),
		Logo:            v("logo"),
		LogoLink:        v("logoLink"),
		Favicon: config.Favicon{
			Default: v("favicon_default"), Size16x16: v("favicon_16"), Size32x32: v("favicon_32"),
			AppleTouch: v("favicon_apple"), Size192x192: v("favicon_192"), Size512x512: v("favicon_512"),
		},
		AnalyticsEnabled:  r.PostFormValue("analyticsEnabled") == "on",
		AnalyticsProvider: v("analyticsProvider"),
		AnalyticsScript:   v("analyticsScript"),
		AnalyticsSite:     v("analyticsSite"),
	}
	if s := v("itemsPerPage"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return g, err
		}
		g.ItemsPerPage = n
	}
	return g, nil
}

func (g generalForm) toSettings() settings.General {
	return settings.General{
		BaseURL:              &g.BaseURL,
		DefaultInterval:      &g.DefaultInterval,
		ItemsPerPage:         &g.ItemsPerPage,
		DashboardHeadingShow: &g.HeadingShow,
		SubtitleShow:         &g.SubtitleShow,
		Footer:               &g.Footer,
		TimeFormat:           &g.TimeFormat,
		DateFormat:           &g.DateFormat,
		Logo:                 &g.Logo,
		LogoLink:             &g.LogoLink,
		Favicon:              &g.Favicon,
		Analytics: &settings.Analytics{
			Enabled: g.AnalyticsEnabled, Provider: g.AnalyticsProvider, ScriptURL: g.AnalyticsScript, SiteID: g.AnalyticsSite,
		},
	}
}

func (a *Admin) settingsForm(w http.ResponseWriter, r *http.Request) {
	a.renderSettings(w, r, map[string]any{})
}

func (a *Admin) renderSettings(w http.ResponseWriter, r *http.Request, data map[string]any) {
	if _, ok := data["General"]; !ok {
		data["General"] = generalFromConfig(a.deps.Config())
	}
	data["Overridden"] = a.deps.Settings.Settings().General != (settings.General{})
	data["PasswordFromSettings"] = a.deps.Settings.Settings().AdminPasswordBcrypt != ""
	data["SettingsPath"] = a.deps.Settings.Path()
	a.exampleData(data)
	a.render(w, r, "settings.html", data)
}

// exampleData adds the example endpoint of config.yaml and, when it cannot be removed yet, why not.
func (a *Admin) exampleData(data map[string]any) {
	cfg := a.deps.Config()
	ex, err := config.FindExampleEndpoint(cfg.Path)
	if err != nil || ex == nil {
		return
	}
	file := filepath.Base(ex.File)
	if rel, err := filepath.Rel(cfg.Path, ex.File); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
		file = filepath.ToSlash(rel)
	}
	data["Example"] = ex
	data["ExampleFile"] = file
	switch {
	case len(cfg.Imports) == 0:
		data["ExampleBlocked"] = "Add a local table, or a database with a table or view, first."
	case ex.OtherEndpoints < 1:
		data["ExampleBlocked"] = "Gatus does not start without endpoints. Wait until your tables have generated endpoints (or run a sync), then remove it."
	}
}

func (a *Admin) exampleDelete(w http.ResponseWriter, r *http.Request) {
	cfg := a.deps.Config()
	if len(cfg.Imports) == 0 {
		redirect(w, r, "/settings", "Add a local table, or a database with a table or view, before removing the example endpoint.")
		return
	}
	ex, err := config.RemoveExampleEndpoint(cfg.Path)
	if err != nil {
		redirect(w, r, "/settings", "Removing the example endpoint failed: "+err.Error())
		return
	}
	redirect(w, r, "/settings", fmt.Sprintf("Example endpoint %s removed from %s. Gatus drops it within 30 seconds.", ex.Label(), filepath.Base(ex.File)))
}

func (a *Admin) settingsSave(w http.ResponseWriter, r *http.Request) {
	g, err := generalFromForm(r)
	if err != nil {
		a.renderSettings(w, r, map[string]any{"General": g, "Error": "Items per page must be a number."})
		return
	}
	err = a.deps.Settings.Update(func(s *settingsT) error {
		s.General = g.toSettings()
		return nil
	})
	if err != nil {
		a.renderSettings(w, r, map[string]any{"General": g, "Error": err.Error()})
		return
	}
	redirect(w, r, "/settings", "Settings saved. The dashboard uses them right away.")
}

func (a *Admin) settingsReset(w http.ResponseWriter, r *http.Request) {
	err := a.deps.Settings.Update(func(s *settingsT) error {
		s.General = settings.General{}
		return nil
	})
	msg := "The settings from config.yaml are used again."
	if err != nil {
		msg = "Reset failed: " + err.Error()
	}
	redirect(w, r, "/settings", msg)
}
