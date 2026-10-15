package settings

import (
	"strings"
	"testing"
	"time"

	"gatusmod/internal/config"
)

func backupSettings() Settings {
	footer := "<p>Powered by \"GatusMOD\"</p>"
	items := 6
	return Settings{
		General:   General{Footer: &footer, ItemsPerPage: &items},
		Databases: []config.Connection{{ID: "d1", Name: "NocoDB", Provider: "nocodb", URL: "http://nocodb", Token: "secret-token"}},
		Imports: []config.Import{
			{ID: "v1", Name: "sites", DatabaseID: "d1", TableID: "m123", Type: "http", Fields: config.Fields{Name: "Title", URL: "Link"}, Interval: "5m"},
			{ID: "l1", Name: "mine", Provider: config.ProviderLocal, Type: "custom", Fields: config.LocalFields, Columns: []string{"url"},
				Rows: []config.LocalRow{{"name": "web", "url": "https://example.org", "group": "2026-01-01", "disabled": "true", "conditions": "[STATUS] == 200\n[RESPONSE_TIME] < 500"}}},
		},
		AdminPasswordBcrypt: "hash",
		ProbeSecret:         "probe",
	}
}

func TestBackup_RoundTrip(t *testing.T) {
	s := backupSettings()
	data, err := NewBackup(s, true, time.Date(2026, 10, 14, 23, 0, 0, 0, time.UTC)).YAML()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "hash") || strings.Contains(text, "{") {
		t.Errorf("expected block-style YAML without the admin password:\n%s", text)
	}
	b, err := ParseBackup(data)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if !b.Tokens || b.Databases[0].Token != "secret-token" || b.ProbeSecret != "probe" || *b.General.Footer != *s.General.Footer {
		t.Errorf("unexpected backup %+v", b)
	}
	row := b.Imports[1].Rows[0]
	if row["group"] != "2026-01-01" || row["disabled"] != "true" || row["conditions"] != s.Imports[1].Rows[0]["conditions"] {
		t.Errorf("cells must come back as the same text: %+v", row)
	}
	if b.Imports[0].Fields.URL != "Link" || b.Imports[0].Interval != "5m" {
		t.Errorf("view settings lost: %+v", b.Imports[0])
	}

	data, _ = NewBackup(s, false, time.Now()).YAML()
	if strings.Contains(string(data), "secret-token") {
		t.Error("tokens must be left out unless asked for")
	}
	if s.Databases[0].Token != "secret-token" {
		t.Error("making a backup must not change the settings")
	}
}

func TestParseBackup_Rejects(t *testing.T) {
	for name, input := range map[string]string{
		"not yaml":  "a: [",
		"gatus":     "endpoints:\n  - name: x\n",
		"newer":     "gatusmodBackup: 99\n",
		"wrong type": "gatusmodBackup: 1\ndatabases: nope\n",
	} {
		if _, err := ParseBackup([]byte(input)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRestore_Replace(t *testing.T) {
	b := NewBackup(backupSettings(), false, time.Now())
	s := Settings{
		Databases:           []config.Connection{{ID: "d1", Name: "NocoDB", Provider: "nocodb", URL: "http://old", Token: "kept"}},
		Imports:             []config.Import{{ID: "gone", Name: "old", DatabaseID: "d1", TableID: "x"}},
		AdminPasswordBcrypt: "mine",
		ProbeSecret:         "current",
	}
	res := s.Restore(b, true)
	if len(s.Databases) != 1 || s.Databases[0].Token != "kept" || s.Databases[0].URL != "http://nocodb" {
		t.Errorf("the database should be restored and keep its token: %+v", s.Databases)
	}
	if len(s.Imports) != 2 || len(res.Removed) != 1 || res.Removed[0] != "gone" {
		t.Errorf("replace should drop what is not in the backup: %+v %+v", s.Imports, res)
	}
	if s.AdminPasswordBcrypt != "mine" || s.ProbeSecret != "probe" {
		t.Error("replace keeps the password and takes the probe secret of the backup")
	}
	if res.DatabasesUpdated != 1 || res.ViewsAdded != 1 || res.TablesAdded != 1 || len(res.MissingTokens) != 0 {
		t.Errorf("unexpected counts %+v", res)
	}
}

func TestRestore_Merge(t *testing.T) {
	b := NewBackup(backupSettings(), false, time.Now())
	interval := "30s"
	s := Settings{
		General:   General{DefaultInterval: &interval},
		Databases: []config.Connection{{ID: "other", Name: "nocodb", Provider: "nocodb", URL: "http://old", Token: "kept"}},
		Imports: []config.Import{
			{ID: "x1", Name: "Sites", DatabaseID: "other", TableID: "old"},
			{ID: "x2", Name: "extra", DatabaseID: "other", TableID: "t2"},
		},
		ProbeSecret: "current",
	}
	res := s.Restore(b, false)
	if len(s.Databases) != 1 || s.Databases[0].ID != "other" || s.Databases[0].Token != "kept" || s.Databases[0].URL != "http://nocodb" {
		t.Errorf("the database with the same name should be updated in place: %+v", s.Databases)
	}
	if len(s.Imports) != 3 || s.Imports[0].ID != "x1" || s.Imports[0].TableID != "m123" || s.Imports[0].DatabaseID != "other" {
		t.Errorf("the view with the same name should be updated and linked to the existing database: %+v", s.Imports)
	}
	if s.ProbeSecret != "current" || *s.General.DefaultInterval != "30s" || *s.General.ItemsPerPage != 6 {
		t.Errorf("merge keeps the probe secret and unset settings: %+v", s.General)
	}
	if res.ViewsUpdated != 1 || res.TablesAdded != 1 || len(res.Removed) != 0 {
		t.Errorf("unexpected counts %+v", res)
	}

	fresh := Settings{}
	res = fresh.Restore(b, false)
	if len(res.MissingTokens) != 1 || res.MissingTokens[0] != "NocoDB" {
		t.Errorf("a database without token should be reported: %+v", res)
	}
}

func TestRestore_ValidThroughUpdate(t *testing.T) {
	src, err := Open(t.TempDir(), baseConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	data, err := NewBackup(src.Settings(), true, time.Now()).YAML()
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseBackup(data)
	if err != nil {
		t.Fatal(err)
	}
	base := baseConfig(t)
	base.Database = nil
	dst, err := Open(t.TempDir(), base)
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Update(func(s *Settings) error { s.Restore(b, true); return nil }); err != nil {
		t.Fatal(err)
	}
	imps := dst.Get().Imports
	if len(imps) != 1 || imps[0].Token != "tok" || dst.Get().ProbeSecret != src.Get().ProbeSecret {
		t.Errorf("restored configuration differs: %+v", imps)
	}
}
