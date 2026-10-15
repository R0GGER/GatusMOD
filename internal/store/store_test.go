package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gatusmod/internal/config"
)

func TestNocoDB(t *testing.T) {
	var created, updated []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("xc-token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v2/tables/t1/records" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"list":[{"Id":7,"name":"api","group":"core","url":"https://x","conditions":"[STATUS] == 200","disabled":null}],"pageInfo":{"isLastPage":true}}`))
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&created)
		case http.MethodPatch:
			_ = json.NewDecoder(r.Body).Decode(&updated)
		}
	}))
	defer srv.Close()
	s, err := New(&config.Database{Provider: "nocodb", URL: srv.URL + "/", Token: "tok", TableID: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	records, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "7" || records[0].Name != "api" || records[0].Disabled {
		t.Fatalf("unexpected records %+v", records)
	}
	records[0].Disabled = true
	if err := s.Update(ctx, records); err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0]["Id"] != float64(7) || updated[0]["disabled"] != true {
		t.Errorf("unexpected update payload %+v", updated)
	}
	if err := s.Create(ctx, []Record{{Name: "new", URL: "https://y"}}); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0]["name"] != "new" {
		t.Errorf("unexpected create payload %+v", created)
	}
}

func TestBaserow(t *testing.T) {
	var batch map[string][]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("user_field_names") != "true" {
			t.Error("expected user_field_names=true")
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("page") == "1":
			_, _ = w.Write([]byte(`{"count":2,"next":"http://next","results":[{"id":1,"name":"a","url":"https://a","disabled":false}]}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"count":2,"next":null,"results":[{"id":2,"name":"b","url":"https://b","disabled":true}]}`))
		case strings.HasSuffix(r.URL.Path, "/batch/"):
			_ = json.NewDecoder(r.Body).Decode(&batch)
		}
	}))
	defer srv.Close()
	s, _ := New(&config.Database{Provider: "baserow", URL: srv.URL, Token: "tok", TableID: "42"})
	records, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].ID != "2" || !records[1].Disabled {
		t.Fatalf("unexpected records %+v", records)
	}
	if err := s.Update(context.Background(), records[:1]); err != nil {
		t.Fatal(err)
	}
	if len(batch["items"]) != 1 || batch["items"][0]["id"] != float64(1) {
		t.Errorf("unexpected batch payload %+v", batch)
	}
}

func TestErrorIncludesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	s, _ := New(&config.Database{Provider: "nocodb", URL: srv.URL, TableID: "t"})
	if _, err := s.List(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected HTTP 403 error, got %v", err)
	}
}

func TestClient_ViewsAndFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/database/rows/table/156/":
			if r.URL.Query().Get("view_id") != "649" {
				t.Errorf("expected view_id=649, got %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"next":null,"results":[{"id":1,"Host":"a","Country":{"id":3,"value":"NL"}}]}`))
		case "/api/database/fields/table/156/":
			_, _ = w.Write([]byte(`[{"name":"Host","type":"text"},{"name":"Country","type":"single_select"}]`))
		case "/api/v2/tables/m1/records":
			if r.URL.Query().Get("viewId") != "vw1" {
				t.Errorf("expected viewId=vw1, got %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"list":[{"Id":1,"Host":"b"}],"pageInfo":{"isLastPage":true}}`))
		case "/api/v2/meta/tables/m1":
			_, _ = w.Write([]byte(`{"columns":[{"title":"Id","uidt":"ID"},{"title":"Host","uidt":"SingleLineText"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	baserow, _ := NewClient(&config.Import{Provider: "baserow", URL: srv.URL + "/api/", TableID: "156", ViewID: "649"})
	rows, err := baserow.Rows(ctx)
	if err != nil || len(rows) != 1 || Text(rows[0]["Country"]) != "NL" {
		t.Fatalf("unexpected baserow rows %+v (%v)", rows, err)
	}
	fields, err := baserow.Fields(ctx)
	if err != nil || len(fields) != 2 || fields[1].Name != "Country" {
		t.Errorf("unexpected baserow fields %+v (%v)", fields, err)
	}
	nocodb, _ := NewClient(&config.Import{Provider: "nocodb", URL: srv.URL + "/api/v2", TableID: "m1", ViewID: "vw1"})
	if rows, err := nocodb.Rows(ctx); err != nil || len(rows) != 1 || nocodb.IDField() != "Id" {
		t.Errorf("unexpected nocodb rows %+v (%v)", rows, err)
	}
	if fields, err := nocodb.Fields(ctx); err != nil || len(fields) != 2 || fields[1].Name != "Host" {
		t.Errorf("unexpected nocodb fields %+v (%v)", fields, err)
	}
}

func TestRecordStore_MappedColumns(t *testing.T) {
	f := config.Fields{Name: "Title", URL: "Address"}
	row := RowFromRecord(Record{Name: "a", URL: "https://a", Group: "dropped"}, f)
	if len(row) != 2 || row["Title"] != "a" || row["Address"] != "https://a" {
		t.Errorf("only mapped columns should be written: %+v", row)
	}
	if r := RecordFromRow(Row{"id": 3.0, "Title": " a ", "Address": "https://a"}, f, "id"); r.ID != "3" || r.Name != "a" || r.Group != "" {
		t.Errorf("unexpected record %+v", r)
	}
}

func TestChunks(t *testing.T) {
	got := chunks([]int{1, 2, 3, 4, 5}, 2)
	if len(got) != 3 || len(got[2]) != 1 {
		t.Errorf("unexpected chunks %v", got)
	}
}
