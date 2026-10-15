package store

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"gatusmod/internal/config"

	"gopkg.in/yaml.v3"
)

var errLocalReadOnly = errors.New("local tables are edited on the admin page")

// Local reads the rows of a local table from the import itself. The basic columns keep their names
// (config.LocalFields); every other chosen column is combined into one `extra` YAML mapping.
type Local struct {
	columns []string
	rows    []config.LocalRow
}

func NewLocal(imp *config.Import) *Local { return &Local{columns: imp.Columns, rows: imp.Rows} }

func (l *Local) IDField() string { return "id" }

func (l *Local) Fields(context.Context) ([]Field, error) {
	fields := make([]Field, 0, len(l.columns))
	for _, key := range l.columns {
		c, _ := config.LocalColumnByKey(key)
		fields = append(fields, Field{Name: key, Type: c.Kind})
	}
	return fields, nil
}

func (l *Local) Create(context.Context, []Row) error { return errLocalReadOnly }
func (l *Local) Update(context.Context, []Row) error { return errLocalReadOnly }

func (l *Local) Rows(context.Context) ([]Row, error) {
	out := make([]Row, 0, len(l.rows))
	for i, lr := range l.rows {
		out = append(out, l.row(i+1, lr))
	}
	return out, nil
}

// row uses the row number as id, so problems read "row id 3".
func (l *Local) row(n int, lr config.LocalRow) Row {
	row := Row{"id": strconv.Itoa(n)}
	extra := map[string]any{}
	chosen := map[string]bool{}
	for _, key := range l.columns {
		chosen[key] = true
	}
	// A cell that is not valid YAML is passed on as is, so the row is reported as invalid instead of silently changed.
	if raw := strings.TrimSpace(lr["extra"]); chosen["extra"] && raw != "" {
		if err := yaml.Unmarshal([]byte(raw), &extra); err != nil {
			row["extra"] = raw
			return withLegacy(row, lr, chosen)
		}
		if extra == nil {
			extra = map[string]any{}
		}
	}
	for _, key := range l.columns {
		value := strings.TrimSpace(lr[key])
		if key == "extra" || config.IsLegacyColumn(key) || value == "" {
			continue
		}
		c, _ := config.LocalColumnByKey(key)
		var v any = value
		switch c.Kind {
		case config.KindBool:
			v = Bool(value)
		case config.KindMultiline:
			v = lr[key]
		case config.KindYAML:
			parsed, err := config.ParseYAMLCell(c, value)
			if err != nil {
				row["extra"] = value
				return withLegacy(row, lr, chosen)
			}
			v = parsed
		}
		setPath(extra, strings.Split(key, "."), v)
	}
	if len(extra) > 0 {
		data, err := yaml.Marshal(extra)
		if err == nil {
			row["extra"] = string(data)
		}
	}
	return withLegacy(row, lr, chosen)
}

func withLegacy(row Row, lr config.LocalRow, chosen map[string]bool) Row {
	for key := range chosen {
		if !config.IsLegacyColumn(key) {
			continue
		}
		if key == "enabled" {
			if v, ok := lr[key]; ok {
				row[key] = Bool(v)
			}
			continue
		}
		row[key] = lr[key]
	}
	return row
}

// setPath sets m[a][b]... = v, creating nested mappings and keeping their other keys.
func setPath(m map[string]any, path []string, v any) {
	for _, p := range path[:len(path)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[path[len(path)-1]] = v
}
