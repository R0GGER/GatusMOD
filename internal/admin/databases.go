package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"gatusmod/internal/config"
	"gatusmod/internal/mapping"
	"gatusmod/internal/settings"
	"gatusmod/internal/store"
	"gatusmod/internal/syncer"
)

const (
	testTimeout  = 30 * time.Second
	previewLimit = 10
)

type fieldRow struct {
	Key, Label, Value, Help string
}

func fieldRows(f config.Fields) []fieldRow {
	enabledHelp := "Column with the status: true (checked) = enabled, false (unchecked) = disabled. Empty: every row is enabled."
	if f.Enabled == "" && f.Disabled != "" {
		enabledHelp += fmt.Sprintf(" Still in use: the old mapping of column %q, where true = disabled. Enter a column here to replace it.", f.Disabled)
	}
	return []fieldRow{
		{"name", "Name", f.Name, "Column with the endpoint name. Empty: the name template is used (default {host})."},
		{"group", "Group", f.Group, "Column with the group. Empty: the group of the table/view."},
		{"url", "URL", f.URL, "Column with a full URL; a value in it wins over the URL template."},
		{"host", "Host", f.Host, "Column with the host, used as {host} in the templates."},
		{"port", "Port", f.Port, "Column with the port, used as {port}; a value like 5201-5210 checks the first port (iperf3 tries the next one while a port is busy). Empty: default ports."},
		{"method", "Method", f.Method, "Column with the HTTP method (GET, POST, ...)."},
		{"interval", "Interval", f.Interval, "Column with a per-row interval (values like 30s or 1m). Not the interval itself: that goes under Schedule. Empty: the interval of the table/view."},
		{"conditions", "Conditions", f.Conditions, "Column with conditions, one per line. Empty: the conditions of the table/view."},
		{"enabled", "Status", f.Enabled, enabledHelp},
		{"extra", "Extra (YAML)", f.Extra, "Column with other Gatus settings as YAML: alerts, headers, body, client, ... For checks by GatusMOD only alerts, heartbeat and maintenance-windows are used."},
	}
}

// typeHint holds what the view form shows for one primary check, so it can follow a change of the check.
type typeHint struct {
	Type, DefaultURL, DefaultConditions string
	Examples                            []config.URLExample
}

func typeHints() []typeHint {
	hints := make([]typeHint, 0, len(config.ImportTypes))
	for _, t := range config.ImportTypes {
		hints = append(hints, typeHint{t, config.DefaultURLTemplate(t), config.DefaultConditions(t), config.URLTemplateExamples(t)})
	}
	return hints
}

func fieldsFromForm(r *http.Request) config.Fields {
	v := func(key string) string { return strings.TrimSpace(r.PostFormValue("field_" + key)) }
	return config.Fields{
		Name: v("name"), Group: v("group"), URL: v("url"), Host: v("host"), Port: v("port"), Method: v("method"),
		Interval: v("interval"), Conditions: v("conditions"), Enabled: v("enabled"), Extra: v("extra"),
	}
}

func viewFromForm(r *http.Request, databaseID string) config.Import {
	v := func(key string) string { return strings.TrimSpace(r.PostFormValue(key)) }
	return config.Import{
		ID:             v("id"),
		Name:           v("name"),
		DatabaseID:     databaseID,
		Group:          v("group"),
		TableID:        v("tableId"),
		ViewID:         v("viewId"),
		Type:           v("type"),
		Fields:         fieldsFromForm(r),
		URLTemplate:    v("urlTemplate"),
		NameTemplate:   v("nameTemplate"),
		DefaultPorts:   v("defaultPorts"),
		Conditions:     strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("conditions"), "\r\n", "\n")),
		Interval:       v("interval"),
		ManualSchedule: r.PostFormValue("manualSchedule") == "on",
		Cron:           v("cron"),
	}
}

func viewPath(imp config.Import) string {
	return "/databases/" + imp.DatabaseID + "/views/" + imp.ID
}

type databaseRow struct {
	ID, Name, Provider, URL string
	Views                   []importRow
	ConfirmDelete           string
}

func (a *Admin) databasesList(w http.ResponseWriter, r *http.Request) {
	cfg := a.deps.Config()
	rows := a.importRows(cfg)
	var databases []databaseRow
	for _, c := range a.deps.Settings.Settings().Databases {
		row := databaseRow{ID: c.ID, Name: c.Name, Provider: config.ProviderTitle(c.Provider), URL: c.URL}
		var files []string
		for _, v := range rows {
			if v.DatabaseID == c.ID {
				row.Views = append(row.Views, v)
				files = append(files, v.FileName)
			}
		}
		row.ConfirmDelete = fmt.Sprintf("Delete database %s and its %d tables/views?", c.Name, len(row.Views))
		if len(files) > 0 {
			row.ConfirmDelete += " These files are deleted and their endpoints are removed from Gatus: " + strings.Join(files, ", ")
		}
		databases = append(databases, row)
	}
	sort.SliceStable(databases, func(i, j int) bool { return strings.ToLower(databases[i].Name) < strings.ToLower(databases[j].Name) })
	a.render(w, r, "databases.html", map[string]any{"Config": cfg, "Databases": databases, "Orphans": syncer.OrphanedFiles(cfg)})
}

func (a *Admin) databaseNew(w http.ResponseWriter, r *http.Request) {
	a.renderDatabaseForm(w, r, config.Connection{Provider: "nocodb"}, false, map[string]any{})
}

func (a *Admin) databaseEdit(w http.ResponseWriter, r *http.Request) {
	c, ok := a.deps.Settings.Settings().Database(r.PathValue("id"))
	if !ok {
		redirect(w, r, "/databases", "That database no longer exists.")
		return
	}
	a.renderDatabaseForm(w, r, c, c.Token != "", map[string]any{})
}

// renderDatabaseForm never shows the token: the form only says whether one is stored.
func (a *Admin) renderDatabaseForm(w http.ResponseWriter, r *http.Request, c config.Connection, hasToken bool, data map[string]any) {
	c.Token = ""
	data["Config"] = a.deps.Config()
	data["Database"] = c
	data["IsNew"] = c.ID == ""
	data["HasToken"] = hasToken
	data["Providers"] = config.Providers
	a.render(w, r, "database_edit.html", data)
}

func (a *Admin) databaseSave(w http.ResponseWriter, r *http.Request) {
	v := func(key string) string { return strings.TrimSpace(r.PostFormValue(key)) }
	c := config.Connection{ID: v("id"), Name: v("name"), Provider: v("provider"), URL: v("url"), Token: v("token")}
	existing, found := a.deps.Settings.Settings().Database(c.ID)
	if c.ID != "" && !found {
		redirect(w, r, "/databases", "That database no longer exists.")
		return
	}
	// A token typed in this request is echoed back so a failed save does not lose it;
	// a stored token is never sent to the browser.
	typedToken := c.Token
	if c.Token == "" {
		c.Token = existing.Token
	}
	isNew := c.ID == ""
	if isNew {
		c.ID = settings.NewID()
	}
	err := a.deps.Settings.Update(func(s *settingsT) error {
		for i := range s.Databases {
			if s.Databases[i].ID == c.ID {
				s.Databases[i] = c
				return nil
			}
		}
		s.Databases = append(s.Databases, c)
		return nil
	})
	if err != nil {
		if isNew {
			c.ID = ""
		}
		a.renderDatabaseForm(w, r, c, c.Token != "", map[string]any{"Error": err.Error(), "TypedToken": typedToken})
		return
	}
	if isNew {
		redirect(w, r, "/databases/"+c.ID+"/views/new", fmt.Sprintf("Database %q saved. Add a table or view of it.", c.Name))
		return
	}
	redirect(w, r, "/databases", fmt.Sprintf("Database %q saved.", c.Name))
}

func (a *Admin) databaseDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var name string
	var views []string
	err := a.deps.Settings.Update(func(s *settingsT) error {
		c, ok := s.Database(id)
		if !ok {
			return errors.New("it no longer exists")
		}
		name = c.Name
		kept := s.Imports[:0:0]
		for _, imp := range s.Imports {
			if imp.DatabaseID == id {
				views = append(views, imp.ID)
			} else {
				kept = append(kept, imp)
			}
		}
		s.Imports = kept
		for i := range s.Databases {
			if s.Databases[i].ID == id {
				s.Databases = append(s.Databases[:i], s.Databases[i+1:]...)
				break
			}
		}
		return nil
	})
	if err != nil {
		redirect(w, r, "/databases", "Deleting failed: "+err.Error())
		return
	}
	removed, err := a.deps.Syncer.RemoveFiles(a.deps.Config(), views...)
	msg := fmt.Sprintf("Database %q and its %d tables/views deleted; their endpoints are removed from Gatus.", name, len(views))
	if len(removed) > 0 {
		msg += " Deleted files: " + strings.Join(removed, ", ") + "."
	}
	if err != nil {
		msg += " Deleting a file failed: " + err.Error()
	}
	redirect(w, r, "/databases", msg)
}

func (a *Admin) viewNew(w http.ResponseWriter, r *http.Request) {
	c, ok := a.deps.Settings.Settings().Database(r.PathValue("id"))
	if !ok {
		redirect(w, r, "/databases", "That database no longer exists.")
		return
	}
	a.renderViewForm(w, r, c, config.Import{DatabaseID: c.ID, Type: config.TypeHTTP}, map[string]any{})
}

func (a *Admin) viewEdit(w http.ResponseWriter, r *http.Request) {
	s := a.deps.Settings.Settings()
	imp, ok := s.Import(r.PathValue("vid"))
	c, dbOK := s.Database(r.PathValue("id"))
	if !ok || !dbOK || imp.DatabaseID != c.ID {
		redirect(w, r, "/databases", "That table or view no longer exists.")
		return
	}
	a.renderViewForm(w, r, c, imp, map[string]any{})
}

// renderViewForm shows a table or view of a database. The token of the database is never passed to the template.
func (a *Admin) renderViewForm(w http.ResponseWriter, r *http.Request, c config.Connection, imp config.Import, data map[string]any) {
	cfg := a.deps.Config()
	c.Token = ""
	imp.DatabaseName = c.Name
	data["Config"] = cfg
	data["Database"] = c
	data["ProviderTitle"] = config.ProviderTitle(c.Provider)
	data["Import"] = imp
	data["IsNew"] = imp.ID == ""
	data["Label"] = imp.Label()
	if imp.Name != "" {
		data["FileName"] = imp.FileName()
	}
	data["Fields"] = fieldRows(imp.Fields)
	data["Types"] = config.ImportTypes
	data["DefaultURLTemplate"] = config.DefaultURLTemplate(imp.Type)
	data["DefaultConditions"] = config.DefaultConditions(imp.Type)
	data["TypeHints"] = typeHints()
	data["DefaultInterval"] = cfg.Defaults.Interval
	a.render(w, r, "view_edit.html", data)
}

func (a *Admin) viewSave(w http.ResponseWriter, r *http.Request) {
	s := a.deps.Settings.Settings()
	c, ok := s.Database(r.PathValue("id"))
	if !ok {
		redirect(w, r, "/databases", "That database no longer exists.")
		return
	}
	imp := viewFromForm(r, c.ID)
	existing, found := s.Import(imp.ID)
	if imp.ID != "" && (!found || existing.DatabaseID != c.ID) {
		redirect(w, r, "/databases", "That table or view no longer exists.")
		return
	}
	if found && imp.Fields.Enabled == "" {
		imp.Fields.Disabled = existing.Fields.Disabled
	}
	if r.PostFormValue("action") == "test" {
		data := map[string]any{}
		resolved, err := s.Resolve(imp)
		if err != nil {
			data["TestError"] = err.Error()
		} else {
			a.testImport(r.Context(), resolved, data)
		}
		a.renderViewForm(w, r, c, imp, data)
		return
	}
	isNew := imp.ID == ""
	if isNew {
		imp.ID = settings.NewID()
	}
	err := a.deps.Settings.Update(func(s *settingsT) error {
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
		a.renderViewForm(w, r, c, imp, map[string]any{"Error": err.Error()})
		return
	}
	imp.DatabaseName = c.Name
	redirect(w, r, "/databases", fmt.Sprintf("%q saved; its endpoints are written to %s.", imp.Label(), imp.FileName()))
}

// testImport reads the columns and rows and shows the first endpoints the import would generate.
func (a *Admin) testImport(ctx context.Context, imp config.Import, data map[string]any) {
	data["Tested"] = true
	if err := imp.Validate(); err != nil && !errors.Is(err, config.ErrMissingImportName) {
		data["TestError"] = err.Error()
		return
	}
	client, err := store.NewClient(&imp)
	if err != nil {
		data["TestError"] = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	rows, err := client.Rows(ctx)
	if err != nil {
		data["TestError"] = "Reading the rows failed: " + err.Error()
		return
	}
	fields, err := client.Fields(ctx)
	if err != nil {
		data["FieldsWarning"] = "The columns could not be listed (" + err.Error() + "); showing the columns of the first row instead."
		fields = columnsFromRows(rows)
	} else {
		data["MappingWarnings"] = unknownColumns(imp.Fields, fields)
	}
	data["Columns"] = fields
	records, problems := mapping.Records(&imp, rows, client.IDField())
	_, valid, buildProblems := syncer.Build(records, a.deps.Config().Defaults.Interval)
	messages := make([]string, 0, len(problems)+len(buildProblems))
	for _, p := range problems {
		messages = append(messages, p.Row+": "+p.Message)
	}
	for _, p := range buildProblems {
		messages = append(messages, p.String())
	}
	if len(records) > previewLimit {
		records = records[:previewLimit]
	}
	data["Preview"] = records
	data["PreviewRows"] = len(rows)
	data["PreviewValid"] = valid
	data["PreviewProblems"] = messages
}

// unknownColumns lists the mapped settings whose column does not exist in the table.
func unknownColumns(f config.Fields, columns []store.Field) []string {
	if len(columns) == 0 {
		return nil
	}
	known := make(map[string]bool, len(columns))
	for _, c := range columns {
		known[c.Name] = true
	}
	var warnings []string
	for _, row := range fieldRows(f) {
		if row.Value != "" && !known[row.Value] {
			warnings = append(warnings, fmt.Sprintf("%s: the table has no column %q. Enter a column name here, or leave it empty.", row.Label, row.Value))
		}
	}
	return warnings
}

func columnsFromRows(rows []store.Row) []store.Field {
	if len(rows) == 0 {
		return nil
	}
	fields := make([]store.Field, 0, len(rows[0]))
	for name := range rows[0] {
		fields = append(fields, store.Field{Name: name})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields
}

func (a *Admin) viewRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("vid")
	var label string
	for _, imp := range a.deps.Config().Imports {
		if imp.ID == id && imp.DatabaseID == r.PathValue("id") {
			label = imp.Label()
		}
	}
	if label == "" {
		redirect(w, r, "/databases", "That table or view no longer exists.")
		return
	}
	err := a.deps.Syncer.RunImport(r.Context(), id, syncer.TriggerManual)
	st := a.deps.Syncer.Status().Imports[id]
	msg := fmt.Sprintf("%q: %d endpoints from %d rows in %dms.", label, st.Endpoints, st.Rows, st.Duration.Milliseconds())
	if err != nil {
		msg = fmt.Sprintf("%q failed: %v", label, err)
	}
	redirect(w, r, "/databases", msg)
}

func (a *Admin) viewDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("vid")
	imp, ok := a.deps.Settings.Settings().Import(id)
	if !ok || imp.DatabaseID != r.PathValue("id") {
		redirect(w, r, "/databases", "That table or view no longer exists.")
		return
	}
	label, err := a.deleteImport(id)
	if err != nil {
		redirect(w, r, "/databases", "Deleting failed: "+err.Error())
		return
	}
	removed, err := a.deps.Syncer.RemoveFiles(a.deps.Config(), id)
	msg := fmt.Sprintf("%q deleted; its endpoints are removed from Gatus.", label)
	if len(removed) > 0 {
		msg += " Deleted file: " + strings.Join(removed, ", ") + "."
	}
	if err != nil {
		msg += " Deleting the file failed: " + err.Error()
	}
	redirect(w, r, "/databases", msg)
}

// orphanDelete removes a generated file whose import no longer exists; only files from that list can be removed.
func (a *Admin) orphanDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PostFormValue("file")
	for _, f := range syncer.OrphanedFiles(a.deps.Config()) {
		if f.Name != name {
			continue
		}
		msg := fmt.Sprintf("File %s deleted; its endpoints are removed from Gatus.", f.Name)
		if _, err := a.deps.Syncer.RemoveFiles(a.deps.Config(), f.ImportID); err != nil {
			msg = "Deleting failed: " + err.Error()
		}
		redirect(w, r, "/databases", msg)
		return
	}
	redirect(w, r, "/databases", "That file is not an orphaned GatusMOD file.")
}

// deleteImport removes an import or local table and returns its label.
func (a *Admin) deleteImport(id string) (string, error) {
	var label string
	err := a.deps.Settings.Update(func(s *settingsT) error {
		for i, imp := range s.Imports {
			if imp.ID == id {
				resolved, _ := s.Resolve(imp)
				label = resolved.Label()
				s.Imports = append(s.Imports[:i], s.Imports[i+1:]...)
				return nil
			}
		}
		return errors.New("it no longer exists")
	})
	return label, err
}

// legacyImport sends the old /imports/{id} links to the table or view.
func (a *Admin) legacyImport(w http.ResponseWriter, r *http.Request) {
	imp, ok := a.deps.Settings.Settings().Import(r.PathValue("id"))
	switch {
	case ok && imp.Local():
		redirect(w, r, "/endpoints/"+imp.ID, "")
	case ok && imp.DatabaseID != "":
		redirect(w, r, viewPath(imp), "")
	default:
		redirect(w, r, "/databases", "")
	}
}
