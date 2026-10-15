package store

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

const (
	nocodbPageSize  = 1000
	nocodbBatchSize = 100
)

// NocoDB uses the v2 data API: /api/v2/tables/{tableId}/records with an `xc-token` API token.
type NocoDB struct {
	baseURL string
	token   string
	tableID string
	viewID  string
	client  *http.Client
}

func (n *NocoDB) recordsURL() string {
	return fmt.Sprintf("%s/api/v2/tables/%s/records", n.baseURL, url.PathEscape(n.tableID))
}

func (n *NocoDB) headers() map[string]string { return map[string]string{"xc-token": n.token} }

func (n *NocoDB) IDField() string { return "Id" }

func (n *NocoDB) Rows(ctx context.Context) ([]Row, error) {
	var rows []Row
	for offset := 0; ; offset += nocodbPageSize {
		var page struct {
			List     []Row `json:"list"`
			PageInfo struct {
				IsLastPage bool `json:"isLastPage"`
			} `json:"pageInfo"`
		}
		q := url.Values{"limit": {strconv.Itoa(nocodbPageSize)}, "offset": {strconv.Itoa(offset)}}
		if n.viewID != "" {
			q.Set("viewId", n.viewID)
		}
		if err := doJSON(ctx, n.client, http.MethodGet, n.recordsURL()+"?"+q.Encode(), n.headers(), nil, &page); err != nil {
			return nil, err
		}
		rows = append(rows, page.List...)
		if page.PageInfo.IsLastPage || len(page.List) < nocodbPageSize {
			return rows, nil
		}
	}
}

func (n *NocoDB) Fields(ctx context.Context) ([]Field, error) {
	var resp struct {
		Columns []struct {
			Title string `json:"title"`
			UIDT  string `json:"uidt"`
		} `json:"columns"`
	}
	u := fmt.Sprintf("%s/api/v2/meta/tables/%s", n.baseURL, url.PathEscape(n.tableID))
	if err := doJSON(ctx, n.client, http.MethodGet, u, n.headers(), nil, &resp); err != nil {
		return nil, err
	}
	fields := make([]Field, 0, len(resp.Columns))
	for _, c := range resp.Columns {
		fields = append(fields, Field{Name: c.Title, Type: c.UIDT})
	}
	return fields, nil
}

func (n *NocoDB) Create(ctx context.Context, rows []Row) error {
	for _, batch := range chunks(rows, nocodbBatchSize) {
		if err := doJSON(ctx, n.client, http.MethodPost, n.recordsURL(), n.headers(), batch, nil); err != nil {
			return err
		}
	}
	return nil
}

func (n *NocoDB) Update(ctx context.Context, rows []Row) error {
	for _, batch := range chunks(rows, nocodbBatchSize) {
		if err := doJSON(ctx, n.client, http.MethodPatch, n.recordsURL(), n.headers(), batch, nil); err != nil {
			return err
		}
	}
	return nil
}
