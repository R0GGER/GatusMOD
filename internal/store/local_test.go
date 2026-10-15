package store

import (
	"context"
	"testing"

	"gatusmod/internal/config"

	"gopkg.in/yaml.v3"
)

func TestLocalRows(t *testing.T) {
	imp := &config.Import{
		Provider: config.ProviderLocal,
		Columns:  []string{"name", "url", "enabled", "graphql", "headers", "dns.query-type", "dns.query-name", "alerts", "extra"},
		Rows: []config.LocalRow{{
			"name": "dns", "url": "8.8.8.8", "enabled": "false", "graphql": "true",
			"headers":        "X-Test: 1",
			"dns.query-type": "A", "dns.query-name": "example.org",
			"alerts":   "- type: email",
			"extra":    "dns:\n  query-type: MX\n  keep: yes\nui:\n  hide-url: true\n",
			"client":   "insecure: true",
			"interval": "5m",
		}},
	}
	rows, err := NewLocal(imp).Rows(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v, err %v", rows, err)
	}
	row := rows[0]
	if row["id"] != "1" || row["name"] != "dns" || row["url"] != "8.8.8.8" || row["enabled"] != false {
		t.Errorf("basic columns wrong: %v", row)
	}
	if _, ok := row["interval"]; ok {
		t.Error("a column that is not chosen must be ignored")
	}
	var extra map[string]any
	if err := yaml.Unmarshal([]byte(row["extra"].(string)), &extra); err != nil {
		t.Fatal(err)
	}
	dns := extra["dns"].(map[string]any)
	if dns["query-type"] != "A" || dns["query-name"] != "example.org" || dns["keep"] != "yes" {
		t.Errorf("a dedicated column must win over extra and keep its other keys: %v", dns)
	}
	if extra["graphql"] != true || extra["headers"].(map[string]any)["X-Test"] != 1 || len(extra["alerts"].([]any)) != 1 {
		t.Errorf("extra %v", extra)
	}
	if _, ok := extra["client"]; ok {
		t.Error("client is not chosen")
	}
	if extra["ui"].(map[string]any)["hide-url"] != true {
		t.Error("the free extra cell must be kept")
	}
}

func TestRecordFromRow_Enabled(t *testing.T) {
	f := config.Fields{Name: "name", Enabled: "status"}
	cases := []struct {
		row      Row
		disabled bool
	}{
		{Row{"name": "a", "status": true}, false},
		{Row{"name": "a", "status": false}, true},
		{Row{"name": "a", "status": nil}, true},
		{Row{"name": "a", "status": "enabled"}, false},
		{Row{"name": "a"}, false},
	}
	for _, c := range cases {
		if r := RecordFromRow(c.row, f, "id"); r.Disabled != c.disabled {
			t.Errorf("%v: disabled = %v, want %v", c.row, r.Disabled, c.disabled)
		}
	}
	if row := RowFromRecord(Record{Name: "a", Disabled: true}, f); row["status"] != false {
		t.Errorf("a disabled record must write false to the enabled column: %v", row)
	}
	legacy := config.Fields{Name: "name", Disabled: "off"}
	if r := RecordFromRow(Row{"name": "a", "off": true}, legacy, "id"); !r.Disabled {
		t.Error("the old disabled mapping must keep working")
	}
}

func TestLocalRows_HostAndPort(t *testing.T) {
	imp := &config.Import{Columns: []string{"name", "host", "port"}, Rows: []config.LocalRow{{"host": "a.example.org", "port": "5201-5210"}}}
	rows, _ := NewLocal(imp).Rows(context.Background())
	if rows[0]["host"] != "a.example.org" || rows[0]["port"] != "5201-5210" {
		t.Errorf("host and port must be row fields: %v", rows[0])
	}
	if _, ok := rows[0]["extra"]; ok {
		t.Errorf("host and port must not end up in extra: %v", rows[0]["extra"])
	}
}

func TestLocalRows_InvalidYAMLIsPassedOn(t *testing.T) {
	imp := &config.Import{Columns: []string{"name", "url", "headers"}, Rows: []config.LocalRow{{"name": "a", "url": "https://a", "headers": "a: ["}}}
	rows, _ := NewLocal(imp).Rows(context.Background())
	if rows[0]["extra"] != "a: [" {
		t.Errorf("the broken cell must reach the build step, got %v", rows[0]["extra"])
	}
}
