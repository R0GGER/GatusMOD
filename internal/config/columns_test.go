package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeColumns(t *testing.T) {
	got, err := NormalizeColumns([]string{"interval", "dns.query-name", "group", "group", " "})
	if err != nil {
		t.Fatal(err)
	}
	if want := "name,group,interval,dns.query-name"; strings.Join(got, ",") != want {
		t.Errorf("got %v, want %s", got, want)
	}
	if _, err := NormalizeColumns([]string{"nope"}); err == nil {
		t.Error("an unknown column must be rejected")
	}
}

func TestValidateCell(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		ok         bool
	}{
		{"name", "anything", true},
		{"method", "post", true},
		{"method", "FETCH", false},
		{"headers", "Content-Type: application/json", true},
		{"headers", "- a", false},
		{"alerts", "- type: email", true},
		{"alerts", "type: email", false},
		{"extra", "client: [", false},
		{"extra", "", true},
	} {
		if err := ValidateCell(tc.key, tc.value); (err == nil) != tc.ok {
			t.Errorf("ValidateCell(%q, %q) = %v, want ok=%v", tc.key, tc.value, err, tc.ok)
		}
	}
}

func TestImportValidate_Local(t *testing.T) {
	imp := Import{Name: "servers", Provider: ProviderLocal, Type: TypeIPerf3, URL: "https://x", TableID: "1", Columns: []string{"interval", "host"},
		URLTemplate: " iperf3://{host}:{port}?ports={ports} ", NameTemplate: "{host}", DefaultPorts: "5201-5210", ManualSchedule: true, Cron: "@hourly"}
	if err := imp.Validate(); err != nil {
		t.Fatal(err)
	}
	if imp.Fields != LocalFields || imp.URL != "" || imp.TableID != "" || imp.ManualSchedule || imp.Cron != "" {
		t.Errorf("local settings not fixed: %+v", imp)
	}
	if imp.Type != TypeIPerf3 || imp.URLTemplate != "iperf3://{host}:{port}?ports={ports}" || imp.NameTemplate != "{host}" || imp.DefaultPorts != "5201-5210" {
		t.Errorf("the endpoint settings of a local table must be kept: %+v", imp)
	}
	if strings.Join(imp.Columns, ",") != "name,host,interval" {
		t.Errorf("columns %v", imp.Columns)
	}
	if !imp.NextRun("1m", time.Now()).IsZero() || imp.ScheduleText("1m") != "On save" {
		t.Error("a local table is not scheduled")
	}
	imp.Columns = []string{"bogus"}
	if err := imp.Validate(); err == nil {
		t.Error("an unknown column must be rejected")
	}

	legacy := Import{Name: "old", Provider: ProviderLocal, Columns: []string{"url"}}
	if err := legacy.Validate(); err != nil || legacy.Type != TypeCustom {
		t.Errorf("a table without a type is custom: %v %q", err, legacy.Type)
	}
	for name, tc := range map[string]struct {
		imp  Import
		want error
	}{
		"bad type":         {Import{Name: "x", Provider: ProviderLocal, Type: "gopher"}, ErrInvalidImportType},
		"custom no url":    {Import{Name: "x", Provider: ProviderLocal, Type: TypeCustom, Columns: []string{"host"}}, ErrCustomNeedsURL},
		"custom template":  {Import{Name: "x", Provider: ProviderLocal, Type: TypeCustom, URLTemplate: "https://{host}"}, nil},
		"http no url cols": {Import{Name: "x", Provider: ProviderLocal, Type: TypeHTTP, Columns: []string{"host"}}, nil},
	} {
		if err := tc.imp.Validate(); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestImportValidate_LocalDisabledBecomesEnabled(t *testing.T) {
	stored := []LocalRow{{"name": "off", "disabled": "true"}, {"name": "on"}}
	imp := Import{Name: "old", Provider: ProviderLocal, Type: TypeHTTP, Columns: []string{"name", "disabled"}, Rows: stored}
	if err := imp.Validate(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(imp.Columns, ",") != "name,enabled" {
		t.Errorf("columns %v", imp.Columns)
	}
	if imp.Rows[0]["enabled"] != "false" || imp.Rows[1]["enabled"] != "true" || imp.Rows[0]["disabled"] != "" {
		t.Errorf("rows %v", imp.Rows)
	}
	if stored[0]["disabled"] != "true" {
		t.Error("the stored rows must not be changed in place")
	}
}

func TestImportValidate_TableURL(t *testing.T) {
	for _, tc := range []struct{ provider, ref, table, view string }{
		{"nocodb", "m1a2b3c4d5e6f7g", "m1a2b3c4d5e6f7g", ""},
		{"nocodb", "https://app.nocodb.com/#/w8x9y0z1/p1q2r3s4t5u6v7w/m1a2b3c4d5e6f7g?view=vw9z8y7x6w5v4u", "m1a2b3c4d5e6f7g", "vw9z8y7x6w5v4u"},
		{"nocodb", "http://mynocodbserver01:8080/dashboard/#/nc/p1q2r3s4t5u6v7w/m1a2b3c4d5e6f7g", "m1a2b3c4d5e6f7g", ""},
		{"baserow", "https://baserow.example.com/database/12/table/345/678", "345", "678"},
		{"baserow", "345", "345", ""},
	} {
		imp := Import{Name: "x", Provider: tc.provider, URL: "https://db.example.com", TableID: tc.ref, Type: TypeHTTP}
		if err := imp.Validate(); err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if imp.TableID != tc.table || imp.ViewID != tc.view {
			t.Errorf("%s: got table %q view %q, want %q %q", tc.ref, imp.TableID, imp.ViewID, tc.table, tc.view)
		}
	}
	imp := Import{Name: "x", Provider: "nocodb", URL: "https://db.example.com", Type: TypeHTTP}
	if err := imp.Validate(); !errors.Is(err, ErrMissingImportTable) {
		t.Errorf("expected the table ID help, got %v", err)
	}
}
