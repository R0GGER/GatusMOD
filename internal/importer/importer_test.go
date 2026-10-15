package importer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gatusmod/internal/store"
	"gatusmod/internal/syncer"
)

const gatusConfig = `
endpoints:
  - name: website
    group: core
    url: https://example.org
    interval: 5m
    conditions:
      - "[STATUS] == 200"
      - "[RESPONSE_TIME] < 300"
    alerts:
      - type: slack
        send-on-resolved: true
  - name: dns
    url: 8.8.8.8
    enabled: false
    dns:
      query-name: example.org
      query-type: A
    conditions:
      - "[DNS_RCODE] == NOERROR"
  - name: website
    group: core
    url: https://dup.example.org
`

type memStore struct {
	records          []store.Record
	created, updated []store.Record
}

func (m *memStore) List(context.Context) ([]store.Record, error) { return m.records, nil }
func (m *memStore) Create(_ context.Context, r []store.Record) error {
	m.created = append(m.created, r...)
	return nil
}
func (m *memStore) Update(_ context.Context, r []store.Record) error {
	m.updated = append(m.updated, r...)
	return nil
}

func TestParse(t *testing.T) {
	records, problems, err := Parse([]byte(gatusConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(problems) != 1 {
		t.Fatalf("unexpected result %+v %v", records, problems)
	}
	web := records[0]
	if web.Conditions != "[STATUS] == 200\n[RESPONSE_TIME] < 300" || web.Interval != "5m" || !strings.Contains(web.Extra, "send-on-resolved: true") {
		t.Errorf("unexpected website record %+v", web)
	}
	if dns := records[1]; !dns.Disabled || !strings.Contains(dns.Extra, "query-name: example.org") {
		t.Errorf("unexpected dns record %+v", dns)
	}
	if _, _, err := Parse([]byte("ui: {}\n")); !errors.Is(err, ErrNoEndpoints) {
		t.Errorf("expected ErrNoEndpoints, got %v", err)
	}
}

// Importing and then generating must give back equivalent Gatus endpoints.
func TestParse_RoundTrip(t *testing.T) {
	records, _, _ := Parse([]byte(gatusConfig))
	data, count, problems := syncer.Build(records, "")
	if count != 2 || len(problems) != 0 {
		t.Fatalf("round trip failed: %d %v\n%s", count, problems, data)
	}
	for _, want := range []string{"send-on-resolved: true", "query-type: A", "enabled: false", "interval: 5m"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("expected generated YAML to contain %q\n%s", want, data)
		}
	}
}

func TestImport_Upsert(t *testing.T) {
	records, _, _ := Parse([]byte(gatusConfig))
	unchanged := records[1]
	unchanged.ID = "9"
	st := &memStore{records: []store.Record{
		{ID: "5", Name: "website", Group: "core", URL: "https://old.example.org"},
		unchanged,
	}}
	result, err := Import(context.Background(), st, []byte(gatusConfig))
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Updated != 1 || result.Unchanged != 1 || len(result.Problems) != 1 {
		t.Errorf("unexpected result %+v", result)
	}
	if len(st.updated) != 1 || st.updated[0].ID != "5" || st.updated[0].URL != "https://example.org" {
		t.Errorf("unexpected updates %+v", st.updated)
	}
}
