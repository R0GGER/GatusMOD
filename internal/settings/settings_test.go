package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gatusmod/internal/config"
)

func baseConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Path: t.TempDir(), BaseURL: "https://yaml.example.com",
		Defaults: config.Defaults{Interval: "1m"},
		Admin:    &config.Admin{Username: "admin", PasswordBcrypt: "yaml-hash"},
		Database: &config.Database{Provider: "nocodb", URL: "http://nocodb", Token: "tok", TableID: "t", SyncInterval: 30 * time.Second},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOpen_MigratesDatabaseOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	m, err := Open(dir, baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	imps := m.Get().Imports
	if len(imps) != 1 || imps[0].Token != "tok" || imps[0].Fields != config.LegacyFields || imps[0].Cron != "@every 30s" || imps[0].Label() != "NocoDB:database" {
		t.Fatalf("unexpected migrated imports %+v", imps)
	}
	if err := m.Update(func(s *Settings) error { s.Imports = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	m2, err := Open(dir, baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Get().Imports) != 0 {
		t.Error("a deleted migrated import must not come back")
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(m.Path()); info.Mode().Perm() != 0o600 {
			t.Errorf("settings file should be 0600, got %v", info.Mode().Perm())
		}
	}
}

func TestMerge_Overrides(t *testing.T) {
	base := baseConfig(t)
	items, hidden, footer, url := 12, false, "<p>x</p>", "https://admin.example.com"
	s := Settings{
		General: General{
			BaseURL: &url, ItemsPerPage: &items, DashboardHeadingShow: &hidden, Footer: &footer,
			Analytics: &Analytics{Enabled: true, Provider: "umami", ScriptURL: "https://u/s.js", SiteID: "id"},
		},
		AdminPasswordBcrypt: "ui-hash",
	}
	cfg, err := Merge(base, s)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != url || cfg.UI.ItemsPerPage != 12 || cfg.UI.HeadingVisible() || cfg.UI.Footer != footer || cfg.Analytics.SiteID != "id" || cfg.Admin.PasswordBcrypt != "ui-hash" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if base.BaseURL != "https://yaml.example.com" || base.Admin.PasswordBcrypt != "yaml-hash" {
		t.Error("the base configuration must not be modified")
	}
	s.General.Analytics.Enabled = false
	if cfg, _ := Merge(base, s); cfg.Analytics != nil {
		t.Error("disabled analytics should turn analytics off")
	}
	bad := "soon"
	if _, err := Merge(base, Settings{General: General{DefaultInterval: &bad}}); err == nil {
		t.Error("expected a validation error")
	}
}

func TestUpdate_ValidatesAndNotifies(t *testing.T) {
	m, err := Open(t.TempDir(), baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	var notified *config.Config
	m.OnChange(func(cfg *config.Config) { notified = cfg })
	err = m.Update(func(s *Settings) error {
		s.Imports = append(s.Imports, config.Import{ID: "x", Name: "bad", Provider: "mysql", URL: "u", TableID: "t"})
		return nil
	})
	if err == nil || notified != nil || len(m.Settings().Imports) != 1 {
		t.Fatalf("an invalid import must be rejected: %v", err)
	}
	err = m.Update(func(s *Settings) error {
		s.Imports = append(s.Imports, config.Import{ID: "y", Name: " Servers ", Provider: "Baserow", URL: "https://b/api", TableID: "156", ViewID: "649", Type: "TCP"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	imp, _ := m.Settings().Import("y")
	if notified == nil || imp.Name != "Servers" || imp.Provider != "baserow" || imp.Type != "tcp" {
		t.Errorf("expected a normalized, applied import: %+v", imp)
	}
	data, _ := os.ReadFile(m.Path())
	if !strings.Contains(string(data), `"viewId": "649"`) {
		t.Errorf("settings not saved: %s", data)
	}
}

func TestOpen_GroupsImportsIntoDatabases(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"imports":[
		{"id":"a","name":"http:sites","provider":"nocodb","url":"http://n","token":"tok","tableId":"t1","type":"http"},
		{"id":"b","name":"iperf3","provider":"nocodb","url":"http://n","token":"tok","tableId":"t2","type":"iperf3"},
		{"id":"c","name":"iperf3","provider":"nocodb","url":"http://other","tableId":"t3","type":"iperf3"},
		{"id":"l","name":"mine","provider":"local","url":"","tableId":"","type":"custom"}],"migratedDatabase":true}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	base := baseConfig(t)
	base.Database = nil
	m, err := Open(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Settings()
	if len(s.Databases) != 2 || s.Databases[0].Name != "NocoDB" || s.Databases[1].Name != "NocoDB-2" || s.Databases[0].Token != "tok" {
		t.Fatalf("unexpected databases %+v", s.Databases)
	}
	labels := map[string]string{}
	for _, imp := range m.Get().Imports {
		labels[imp.ID] = imp.Label()
		if imp.ID == "b" && (imp.Token != "tok" || imp.URL != "http://n") {
			t.Errorf("the merged import should get the connection of its database: %+v", imp)
		}
	}
	want := map[string]string{"a": "NocoDB:http-sites", "b": "NocoDB:iperf3", "c": "NocoDB-2:iperf3", "l": "mine"}
	for id, label := range want {
		if labels[id] != label {
			t.Errorf("import %s: label %q, want %q", id, labels[id], label)
		}
	}
	data, _ := os.ReadFile(m.Path())
	if strings.Count(string(data), `"token": "tok"`) != 1 {
		t.Errorf("the token must only be stored on the database\n%s", data)
	}
}

func TestUpdate_DatabaseNames(t *testing.T) {
	m, err := Open(t.TempDir(), baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	db := m.Settings().Imports[0].DatabaseID
	err = m.Update(func(s *Settings) error {
		s.Imports = append(s.Imports, config.Import{ID: "x", Name: "DATABASE", DatabaseID: db, TableID: "t2"})
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "already has a table or view") {
		t.Errorf("expected a duplicate view name error, got %v", err)
	}
	err = m.Update(func(s *Settings) error {
		s.Imports = append(s.Imports, config.Import{ID: "x", Name: "other", DatabaseID: "missing", TableID: "t2"})
		return nil
	})
	if err == nil {
		t.Error("an import of a missing database must be rejected")
	}
	err = m.Update(func(s *Settings) error {
		s.Databases = append(s.Databases, config.Connection{ID: "d2", Name: "a:b", Provider: "nocodb", URL: "http://x"})
		return nil
	})
	if err == nil {
		t.Error("a database name with ':' must be rejected")
	}
}

func TestOpen_GeneratesProbeSecretOnce(t *testing.T) {
	dir := t.TempDir()
	m, err := Open(dir, baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	secret := m.Get().ProbeSecret
	if len(secret) != 64 || m.Settings().ProbeSecret != secret {
		t.Fatalf("expected a generated secret, got %q", secret)
	}
	m2, err := Open(dir, baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if m2.Get().ProbeSecret != secret {
		t.Error("the secret must be kept, or every push URL changes")
	}
}

func TestSetBase(t *testing.T) {
	m, err := Open(t.TempDir(), baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	base := baseConfig(t)
	base.BaseURL = "https://new.example.com"
	m.SetBase(base)
	if m.Get().BaseURL != "https://new.example.com" || len(m.Get().Imports) != 1 {
		t.Errorf("new base not applied: %+v", m.Get())
	}
}
