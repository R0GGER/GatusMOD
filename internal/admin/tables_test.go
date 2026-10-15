package admin

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gatusmod/internal/config"
)

func TestTablesCRUD(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	if body := f.get(t, "/endpoints/new").Body.String(); !strings.Contains(body, `name="row.__N__.url"`) || !strings.Contains(body, `name="row.0.name"`) {
		t.Fatalf("the new table form should have a row template and one empty row\n%s", body)
	}
	form := url.Values{
		"name": {"servers"}, "group": {"lab"}, "interval": {"10m"},
		"columns":          {"group", "url", "conditions", "headers"},
		"row.0.name":       {"speed"},
		"row.0.url":        {"iperf3://iperf.example.org:5201"},
		"row.3.name":       {"site"},
		"row.3.group":      {"web"},
		"row.3.url":        {"https://example.org"},
		"row.3.headers":    {"X-Test: 1"},
		"row.3.interval":   {"1m"},
		"row.7.name":       {"  "},
		"row.2.name":       {"bad"},
		"row.2.url":        {"https://bad.example.org"},
		"row.2.headers":    {"a: ["},
		"row.2.conditions": {"[STATUS] == 200"},
	}
	body := f.post(t, "/endpoints/save", form, true).Body.String()
	if !strings.Contains(body, "Fix the marked cells") || !strings.Contains(body, "not a valid YAML mapping") || len(f.mgr.Settings().Imports) != 1 {
		t.Fatalf("an invalid cell must be shown and nothing saved\n%s", body)
	}
	form.Set("row.2.headers", "a: b")
	rec := f.post(t, "/endpoints/save", form, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving failed: %d %s", rec.Code, rec.Body.String())
	}
	imps := f.mgr.Settings().Imports
	if len(imps) != 2 || !imps[1].Local() {
		t.Fatalf("unexpected imports %+v", imps)
	}
	table := imps[1]
	if len(table.Rows) != 3 || table.Rows[0]["name"] != "speed" || table.Rows[1]["name"] != "bad" || table.Rows[2]["name"] != "site" {
		t.Errorf("rows must be kept in form order without empty rows: %+v", table.Rows)
	}
	if _, ok := table.Rows[2]["interval"]; ok {
		t.Error("values of columns that are not chosen must be dropped")
	}

	if err := f.syncer.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.cfgDir, config.DefaultOutputFile))
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{"name: site", "X-Test: 1", "url: https://bad.example.org", "external-endpoints:", "name: speed", "group: lab"} {
		if !strings.Contains(out, want) {
			t.Errorf("generated file should contain %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "iperf3://") {
		t.Error("probe URLs must not be written for Gatus")
	}

	if body := f.get(t, "/databases").Body.String(); strings.Contains(body, "servers") {
		t.Error("local tables must not be listed under the databases")
	}
	if body := f.get(t, "/endpoints").Body.String(); !strings.Contains(body, "servers") || !strings.Contains(body, "/mod/admin/endpoints/"+table.ID) {
		t.Errorf("the table should be listed\n%s", body)
	}
	if rec := f.get(t, "/imports/"+table.ID); rec.Header().Get("Location") != Prefix+"/endpoints/"+table.ID {
		t.Errorf("editing a table as an import should redirect, got %q", rec.Header().Get("Location"))
	}
	if body := f.get(t, "/import").Body.String(); strings.Contains(body, "servers") {
		t.Error("local tables are no YAML import target")
	}
	edit := f.get(t, "/endpoints/"+table.ID).Body.String()
	for _, want := range []string{`value="iperf3://iperf.example.org:5201"`, "3 valid endpoints", `data-col="interval" hidden`} {
		if !strings.Contains(edit, want) {
			t.Errorf("edit page should contain %q", want)
		}
	}

	if rec := f.post(t, "/endpoints/"+table.ID+"/delete", nil, true); rec.Code != http.StatusSeeOther || len(f.mgr.Settings().Imports) != 1 {
		t.Errorf("delete failed: %d %+v", rec.Code, f.mgr.Settings().Imports)
	}
}

func TestTableSave_URLTemplate(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	if body := f.get(t, "/endpoints/new").Body.String(); !strings.Contains(body, `data-type-select`) || !strings.Contains(body, `data-example-template="iperf3://{host}:{port}?ports={ports}"`) {
		t.Fatalf("the new table form should offer the primary check and URL examples\n%s", body)
	}
	form := url.Values{
		"name": {"speed"}, "type": {"iperf3"}, "urlTemplate": {"iperf3://{host}:{port}?ports={ports}"},
		"nameTemplate": {"{host}:{port}"}, "defaultPorts": {"5201-5210"},
		"columns":    {"host", "port"},
		"row.0.host": {"iperf.example.org"},
		"row.1.host": {"other.example.org"}, "row.1.port": {"5300"},
		"action":     {"preview"},
	}
	body := f.post(t, "/endpoints/save", form, true).Body.String()
	for _, want := range []string{"iperf3://iperf.example.org:5201?ports=5201-5210", "iperf3://other.example.org:5300?ports=5300", "iperf.example.org:5201", "2 valid endpoints"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview should contain %q\n%s", want, body)
		}
	}
	if len(f.mgr.Settings().Imports) != 1 {
		t.Fatal("a preview must not save")
	}
	form.Set("action", "save")
	if rec := f.post(t, "/endpoints/save", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving failed: %d %s", rec.Code, rec.Body.String())
	}
	imps := f.mgr.Settings().Imports
	if len(imps) != 2 || imps[1].Type != config.TypeIPerf3 || imps[1].URLTemplate != "iperf3://{host}:{port}?ports={ports}" || imps[1].DefaultPorts != "5201-5210" {
		t.Fatalf("the endpoint settings must be saved: %+v", imps)
	}
	if edit := f.get(t, "/endpoints/"+imps[1].ID).Body.String(); !strings.Contains(edit, `<option value="iperf3" selected>`) || !strings.Contains(edit, "2 valid endpoints") {
		t.Errorf("the edit page should show the saved check\n%s", edit)
	}
}

func TestTableSave_Enabled(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	form := url.Values{
		"name": {"t"}, "columns": {"url", "enabled"},
		"row.0.name": {"on"}, "row.0.url": {"https://on.example.org"}, "row.0.enabled": {"false", "true"},
		"row.1.name": {"off"}, "row.1.url": {"https://off.example.org"}, "row.1.enabled": {"false"},
		"row.2.enabled": {"false", "true"},
	}
	if rec := f.post(t, "/endpoints/save", form, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving failed: %d %s", rec.Code, rec.Body.String())
	}
	rows := f.mgr.Settings().Imports[len(f.mgr.Settings().Imports)-1].Rows
	if len(rows) != 2 || rows[0]["enabled"] != "true" || rows[1]["enabled"] != "false" {
		t.Errorf("checked must be stored as true, unchecked as false, and a row with only enabled dropped: %v", rows)
	}
}

func TestTableSave_ReportsSkippedRows(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.login(t)
	form := url.Values{"name": {"t"}, "columns": {"url"}, "row.0.name": {"no url"}, "row.1.name": {"x"}, "row.1.url": {"ntp://pool.ntp.org"}}
	rec := f.post(t, "/endpoints/save", form, true)
	loc, _ := url.QueryUnescape(rec.Header().Get("Location"))
	if !strings.Contains(loc, "1 endpoints, 1 rows skipped") {
		t.Fatalf("unexpected redirect %q", loc)
	}
	if body := f.get(t, strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], Prefix)).Body.String(); !strings.Contains(body, "row id 1: url is empty") {
		t.Errorf("the edit page should list the skipped row\n%s", body)
	}
}
