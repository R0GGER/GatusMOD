package admin

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gatusmod/internal/config"
	"gatusmod/internal/mapping"
	"gatusmod/internal/settings"
	"gatusmod/internal/store"
	"gatusmod/internal/syncer"
)

type columnOption struct {
	config.LocalColumn
	On bool
}

type columnGroup struct {
	Name    string
	Columns []columnOption
}

type gridCell struct {
	config.LocalColumn
	On    bool
	Name  string
	Value string
	Error string
}

type gridRow struct {
	Cells []gridCell
}

var rowField = regexp.MustCompile(`^row\.(\d+)\.(.+)$`)

func (a *Admin) tablesList(w http.ResponseWriter, r *http.Request) {
	cfg := a.deps.Config()
	var tables []importRow
	for _, row := range a.importRows(cfg) {
		if row.Local() {
			tables = append(tables, row)
		}
	}
	a.render(w, r, "tables.html", map[string]any{"Config": cfg, "Tables": tables})
}

func (a *Admin) tableNew(w http.ResponseWriter, r *http.Request) {
	imp := config.Import{Provider: config.ProviderLocal, Type: config.TypeHTTP, Columns: config.DefaultLocalColumns, Rows: []config.LocalRow{{}}}
	a.renderTableForm(w, r, imp, map[string]any{})
}

func (a *Admin) tableEdit(w http.ResponseWriter, r *http.Request) {
	imp, ok := a.deps.Settings.Settings().Import(r.PathValue("id"))
	if !ok {
		redirect(w, r, "/endpoints", "That table no longer exists.")
		return
	}
	if !imp.Local() {
		redirect(w, r, viewPath(imp), "")
		return
	}
	a.renderTableForm(w, r, imp, map[string]any{})
}

func (a *Admin) tableSave(w http.ResponseWriter, r *http.Request) {
	v := func(key string) string { return strings.TrimSpace(r.PostFormValue(key)) }
	imp := config.Import{
		ID:           v("id"),
		Name:         v("name"),
		Group:        v("group"),
		Provider:     config.ProviderLocal,
		Type:         v("type"),
		URLTemplate:  v("urlTemplate"),
		NameTemplate: v("nameTemplate"),
		DefaultPorts: v("defaultPorts"),
		Interval:     v("interval"),
		Conditions:   strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("conditions"), "\r\n", "\n")),
		Columns:      r.PostForm["columns"],
	}
	existing, found := a.deps.Settings.Settings().Import(imp.ID)
	if imp.ID != "" && (!found || !existing.Local()) {
		redirect(w, r, "/endpoints", "That table no longer exists.")
		return
	}
	cols, err := config.NormalizeColumns(imp.Columns)
	if err != nil {
		a.renderTableForm(w, r, imp, map[string]any{"Error": err.Error()})
		return
	}
	imp.Columns = cols
	rows, cellErrors := rowsFromForm(r, cols)
	imp.Rows = rows
	preview := r.PostFormValue("action") == "preview"
	if len(cellErrors) > 0 {
		msg := "Fix the marked cells; nothing was saved."
		if preview {
			msg = "Fix the marked cells."
		}
		a.renderTableForm(w, r, imp, map[string]any{"Error": msg, "CellErrors": cellErrors, "Previewed": preview})
		return
	}
	if preview {
		a.renderTableForm(w, r, imp, map[string]any{"Previewed": true})
		return
	}
	isNew := imp.ID == ""
	if isNew {
		imp.ID = settings.NewID()
	}
	err = a.deps.Settings.Update(func(s *settingsT) error {
		for i := range s.Imports {
			if s.Imports[i].ID == imp.ID {
				s.Imports[i] = imp
				return nil
			}
		}
		s.Imports = append(s.Imports, imp)
		return nil
	})
	if err != nil {
		if isNew {
			imp.ID = ""
		}
		a.renderTableForm(w, r, imp, map[string]any{"Error": err.Error()})
		return
	}
	_, valid, problems := a.tablePreview(r.Context(), imp)
	msg := fmt.Sprintf("Table %q saved: %d endpoints. Gatus shows changes within 30 seconds.", imp.Name, valid)
	if len(problems) > 0 {
		redirect(w, r, "/endpoints/"+imp.ID, fmt.Sprintf("Table %q saved: %d endpoints, %d rows skipped (see below).", imp.Name, valid, len(problems)))
		return
	}
	redirect(w, r, "/endpoints", msg)
}

func (a *Admin) tableDelete(w http.ResponseWriter, r *http.Request) {
	name, err := a.deleteImport(r.PathValue("id"))
	msg := fmt.Sprintf("Table %q deleted; its endpoints are removed from Gatus.", name)
	if err != nil {
		msg = "Deleting failed: " + err.Error()
	}
	redirect(w, r, "/endpoints", msg)
}

// rowsFromForm reads the cells named row.<n>.<column> of the chosen columns, in the order of n.
// Rows without any value are dropped. Errors are keyed by "<position>.<column>".
func rowsFromForm(r *http.Request, cols []string) ([]config.LocalRow, map[string]string) {
	chosen := map[string]config.LocalColumn{}
	for _, key := range cols {
		c, _ := config.LocalColumnByKey(key)
		chosen[key] = c
	}
	byIndex := map[int]config.LocalRow{}
	for name, values := range r.PostForm {
		m := rowField.FindStringSubmatch(name)
		if m == nil || len(values) == 0 {
			continue
		}
		c, ok := chosen[m[2]]
		if !ok {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		// The enabled checkbox follows a hidden "false", so the last value wins.
		value := strings.ReplaceAll(values[len(values)-1], "\r\n", "\n")
		switch c.Kind {
		case config.KindBool:
			if value != "" {
				value = strconv.FormatBool(store.Bool(value))
			}
		case config.KindMultiline, config.KindYAML:
			value = strings.TrimRight(value, " \n")
		default:
			value = strings.TrimSpace(value)
		}
		if value == "" {
			continue
		}
		if byIndex[n] == nil {
			byIndex[n] = config.LocalRow{}
		}
		byIndex[n][m[2]] = value
	}
	indexes := make([]int, 0, len(byIndex))
	for n := range byIndex {
		indexes = append(indexes, n)
	}
	sort.Ints(indexes)
	rows := make([]config.LocalRow, 0, len(indexes))
	errs := map[string]string{}
	for _, n := range indexes {
		row := byIndex[n]
		if _, ok := row["enabled"]; ok && len(row) == 1 {
			continue
		}
		for key, value := range row {
			if err := config.ValidateCell(key, value); err != nil {
				errs[cellKey(len(rows), key)] = err.Error()
			}
		}
		rows = append(rows, row)
	}
	return rows, errs
}

func cellKey(pos int, col string) string { return strconv.Itoa(pos) + "." + col }

// tablePreview maps the rows the way the syncer does and returns the records, the number of valid endpoints
// and the skipped rows.
func (a *Admin) tablePreview(ctx context.Context, imp config.Import) ([]store.Record, int, []string) {
	if imp.Name == "" {
		imp.Name = "(new table)"
	}
	if err := imp.Validate(); err != nil {
		return nil, 0, []string{err.Error()}
	}
	rows, _ := store.NewLocal(&imp).Rows(ctx)
	records, problems := mapping.Records(&imp, rows, "id")
	_, valid, buildProblems := syncer.Build(records, a.deps.Config().Defaults.Interval)
	messages := make([]string, 0, len(problems)+len(buildProblems))
	for _, p := range problems {
		messages = append(messages, p.Row+": "+p.Message)
	}
	for _, p := range buildProblems {
		messages = append(messages, p.String())
	}
	return records, valid, messages
}

func (a *Admin) renderTableForm(w http.ResponseWriter, r *http.Request, imp config.Import, data map[string]any) {
	cfg := a.deps.Config()
	on := map[string]bool{}
	for _, key := range imp.Columns {
		on[key] = true
	}
	var groups []columnGroup
	header := make([]columnOption, 0, len(config.LocalColumns))
	for _, c := range config.LocalColumns {
		opt := columnOption{LocalColumn: c, On: on[c.Key] || c.Always}
		header = append(header, opt)
		if len(groups) == 0 || groups[len(groups)-1].Name != c.Group {
			groups = append(groups, columnGroup{Name: c.Group})
		}
		groups[len(groups)-1].Columns = append(groups[len(groups)-1].Columns, opt)
	}
	cellErrors, _ := data["CellErrors"].(map[string]string)
	makeRow := func(index string, pos int, values config.LocalRow) gridRow {
		row := gridRow{Cells: make([]gridCell, 0, len(header))}
		for _, h := range header {
			value, ok := values[h.Key]
			if h.Key == "enabled" && !ok {
				value = "true"
			}
			if h.Kind == config.KindBool && !store.Bool(value) {
				value = ""
			}
			row.Cells = append(row.Cells, gridCell{
				LocalColumn: h.LocalColumn, On: h.On, Name: "row." + index + "." + h.Key,
				Value: value, Error: cellErrors[cellKey(pos, h.Key)],
			})
		}
		return row
	}
	rows := make([]gridRow, 0, len(imp.Rows))
	for i, values := range imp.Rows {
		rows = append(rows, makeRow(strconv.Itoa(i), i, values))
	}
	records, valid, problems := a.tablePreview(r.Context(), imp)
	if data["Previewed"] == true {
		if len(records) > previewLimit {
			records = records[:previewLimit]
		}
		data["Preview"] = records
	}
	if imp.Type == "" {
		imp.Type = config.TypeCustom
	}
	data["Config"] = cfg
	data["Table"] = imp
	data["IsNew"] = imp.ID == ""
	data["ColumnGroups"] = groups
	data["Header"] = header
	data["Rows"] = rows
	data["Template"] = makeRow("__N__", -1, nil)
	data["Valid"] = valid
	data["Problems"] = problems
	data["DefaultInterval"] = cfg.Defaults.Interval
	data["Types"] = config.ImportTypes
	data["TypeHints"] = typeHints()
	data["DefaultURLTemplate"] = config.DefaultURLTemplate(imp.Type)
	data["DefaultConditions"] = config.DefaultConditions(imp.Type)
	if imp.ID != "" {
		data["Status"] = a.deps.Syncer.Status().Imports[imp.ID]
	}
	a.render(w, r, "table_edit.html", data)
}
