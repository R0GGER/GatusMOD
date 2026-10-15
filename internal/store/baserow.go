package store

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

const (
	baserowPageSize  = 200
	baserowBatchSize = 200
)

// Baserow uses the database rows API with a database token (`Authorization: Token ...`) and user field names.
type Baserow struct {
	baseURL string
	token   string
	tableID string
	viewID  string
	client  *http.Client
}

func (b *Baserow) headers() map[string]string {
	return map[string]string{"Authorization": "Token " + b.token}
}

func (b *Baserow) IDField() string { return "id" }

func (b *Baserow) Rows(ctx context.Context) ([]Row, error) {
	var rows []Row
	for page := 1; ; page++ {
		var resp struct {
			Next    *string `json:"next"`
			Results []Row   `json:"results"`
		}
		q := url.Values{"user_field_names": {"true"}, "size": {fmt.Sprint(baserowPageSize)}, "page": {fmt.Sprint(page)}}
		if b.viewID != "" {
			q.Set("view_id", b.viewID)
		}
		u := fmt.Sprintf("%s/api/database/rows/table/%s/?%s", b.baseURL, url.PathEscape(b.tableID), q.Encode())
		if err := doJSON(ctx, b.client, http.MethodGet, u, b.headers(), nil, &resp); err != nil {
			return nil, err
		}
		rows = append(rows, resp.Results...)
		if resp.Next == nil || len(resp.Results) == 0 {
			return rows, nil
		}
	}
}

func (b *Baserow) Fields(ctx context.Context) ([]Field, error) {
	var resp []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	u := fmt.Sprintf("%s/api/database/fields/table/%s/", b.baseURL, url.PathEscape(b.tableID))
	if err := doJSON(ctx, b.client, http.MethodGet, u, b.headers(), nil, &resp); err != nil {
		return nil, err
	}
	fields := make([]Field, 0, len(resp))
	for _, f := range resp {
		fields = append(fields, Field{Name: f.Name, Type: f.Type})
	}
	return fields, nil
}

func (b *Baserow) batchURL() string {
	return fmt.Sprintf("%s/api/database/rows/table/%s/batch/?user_field_names=true", b.baseURL, url.PathEscape(b.tableID))
}

func (b *Baserow) Create(ctx context.Context, rows []Row) error {
	for _, batch := range chunks(rows, baserowBatchSize) {
		if err := doJSON(ctx, b.client, http.MethodPost, b.batchURL(), b.headers(), map[string]any{"items": batch}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (b *Baserow) Update(ctx context.Context, rows []Row) error {
	for _, batch := range chunks(rows, baserowBatchSize) {
		if err := doJSON(ctx, b.client, http.MethodPatch, b.batchURL(), b.headers(), map[string]any{"items": batch}, nil); err != nil {
			return err
		}
	}
	return nil
}
