// Package store reads and writes rows of a NocoDB or Baserow table.
//
// Which column holds which Gatus setting is configured per import (config.Fields). The legacy
// `gatusmod.database` table uses these columns (names are case-sensitive):
//
//	name        text      required
//	group       text
//	url         text      required
//	method      text
//	interval    text      e.g. 30s, 5m (empty = defaults.interval)
//	conditions  long text one condition per line
//	disabled    checkbox  true = endpoint is disabled in Gatus (views map an enabled column instead)
//	extra       long text any other Gatus endpoint settings as YAML (alerts, headers, body, client, ...)
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gatusmod/internal/config"
)

// Record is one Gatus endpoint as read from (or written to) a table.
type Record struct {
	ID         string
	Name       string
	Group      string
	URL        string
	Method     string
	Interval   string
	Conditions string
	Disabled   bool
	Extra      string
	// Source and SourceName identify the import the record came from.
	Source     string
	SourceName string
}

// Key identifies an endpoint the same way Gatus does: by group and name.
func (r Record) Key() string { return r.Group + "\x00" + r.Name }

// Row is a table row keyed by column name.
type Row map[string]any

type Field struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Client talks to one table (and optionally one view) of NocoDB or Baserow.
type Client interface {
	Rows(ctx context.Context) ([]Row, error)
	Fields(ctx context.Context) ([]Field, error)
	Create(ctx context.Context, rows []Row) error
	// Update changes existing rows; every row must contain IDField.
	Update(ctx context.Context, rows []Row) error
	IDField() string
}

// EndpointStore reads and writes records, used by the YAML import.
type EndpointStore interface {
	List(ctx context.Context) ([]Record, error)
	Create(ctx context.Context, records []Record) error
	Update(ctx context.Context, records []Record) error
}

func NewClient(imp *config.Import) (Client, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	base := apiBase(imp.URL)
	switch imp.Provider {
	case "nocodb":
		return &NocoDB{baseURL: base, token: imp.Token, tableID: imp.TableID, viewID: imp.ViewID, client: client}, nil
	case "baserow":
		return &Baserow{baseURL: base, token: imp.Token, tableID: imp.TableID, viewID: imp.ViewID, client: client}, nil
	case config.ProviderLocal:
		return NewLocal(imp), nil
	}
	return nil, config.ErrInvalidProvider
}

// New returns a record store for the legacy `gatusmod.database` table.
func New(db *config.Database) (EndpointStore, error) {
	imp := config.FromDatabase(db)
	client, err := NewClient(&imp)
	if err != nil {
		return nil, err
	}
	return NewRecordStore(client, imp.Fields), nil
}

// apiBase accepts both the site URL and the API URL (https://baserow.example.com/api), as both are shown in the database UIs.
func apiBase(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	for _, suffix := range []string{"/api/v2", "/api/v1", "/api"} {
		if strings.HasSuffix(u, suffix) {
			return strings.TrimSuffix(u, suffix)
		}
	}
	return u
}

// RecordStore maps records to the columns given by fields. Only columns with a name are read and written.
type RecordStore struct {
	client Client
	fields config.Fields
}

func NewRecordStore(client Client, fields config.Fields) *RecordStore {
	return &RecordStore{client: client, fields: fields}
}

func (s *RecordStore) List(ctx context.Context) ([]Record, error) {
	rows, err := s.client.Rows(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, RecordFromRow(row, s.fields, s.client.IDField()))
	}
	return records, nil
}

func (s *RecordStore) Create(ctx context.Context, records []Record) error {
	rows := make([]Row, 0, len(records))
	for _, r := range records {
		rows = append(rows, RowFromRecord(r, s.fields))
	}
	return s.client.Create(ctx, rows)
}

func (s *RecordStore) Update(ctx context.Context, records []Record) error {
	rows := make([]Row, 0, len(records))
	for _, r := range records {
		id, err := strconv.Atoi(r.ID)
		if err != nil {
			return fmt.Errorf("invalid row id %q", r.ID)
		}
		row := RowFromRecord(r, s.fields)
		row[s.client.IDField()] = id
		rows = append(rows, row)
	}
	return s.client.Update(ctx, rows)
}

// RecordFromRow reads the mapped columns as they are, without templates or defaults.
func RecordFromRow(row Row, f config.Fields, idField string) Record {
	text := func(col string) string {
		if col == "" {
			return ""
		}
		return strings.TrimSpace(Text(row[col]))
	}
	r := Record{
		ID:       Text(row[idField]),
		Name:     text(f.Name),
		Group:    text(f.Group),
		URL:      text(f.URL),
		Method:   text(f.Method),
		Interval: text(f.Interval),
	}
	if f.Conditions != "" {
		r.Conditions = Text(row[f.Conditions])
	}
	// A missing enabled cell (an unknown column, or a local row saved before the column was chosen) keeps the row enabled.
	if f.Enabled != "" {
		if v, ok := row[f.Enabled]; ok {
			r.Disabled = !Bool(v)
		}
	} else if f.Disabled != "" {
		r.Disabled = Bool(row[f.Disabled])
	}
	if f.Extra != "" {
		r.Extra = Text(row[f.Extra])
	}
	return r
}

func RowFromRecord(r Record, f config.Fields) Row {
	row := Row{}
	set := func(col string, v any) {
		if col != "" {
			row[col] = v
		}
	}
	set(f.Name, r.Name)
	set(f.Group, r.Group)
	set(f.URL, r.URL)
	set(f.Method, r.Method)
	set(f.Interval, r.Interval)
	set(f.Conditions, r.Conditions)
	if f.Enabled != "" {
		set(f.Enabled, !r.Disabled)
	} else {
		set(f.Disabled, r.Disabled)
	}
	set(f.Extra, r.Extra)
	return row
}

// Text converts a cell value to text. Select options, links and lookups (objects or lists of objects) use their value or title.
func Text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	case map[string]any:
		for _, k := range []string{"value", "title", "name"} {
			if s, ok := t[k]; ok {
				return Text(s)
			}
		}
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s := Text(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

func Bool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "yes", "on", "enabled":
			return true
		}
		b, _ := strconv.ParseBool(strings.TrimSpace(t))
		return b
	}
	return false
}

func chunks[T any](items []T, size int) [][]T {
	var out [][]T
	for size < len(items) {
		items, out = items[size:], append(out, items[:size])
	}
	if len(items) > 0 {
		out = append(out, items)
	}
	return out
}

// doJSON sends body (if not nil) as JSON and decodes the response into out (if not nil).
func doJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := string(data)
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, req.URL.Path, resp.StatusCode, snippet)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: decoding response: %w", method, req.URL.Path, err)
	}
	return nil
}
