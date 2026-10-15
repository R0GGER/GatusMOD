package admin

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gatusmod/internal/settings"
)

type backupCounts struct {
	Databases, Views, Tables int
}

// importPage holds what the Backup & import page shows besides the result of a form.
func (a *Admin) importPage(r *http.Request) map[string]any {
	cfg := a.deps.Config()
	s := a.deps.Settings.Settings()
	counts := backupCounts{Databases: len(s.Databases)}
	for _, imp := range s.Imports {
		if imp.Local() {
			counts.Tables++
		} else {
			counts.Views++
		}
	}
	return map[string]any{
		"Config": cfg, "Targets": yamlTargets(cfg), "Target": r.FormValue("target"), "Counts": counts,
	}
}

func (a *Admin) backupExport(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	data, err := settings.NewBackup(a.deps.Settings.Settings(), r.PostFormValue("tokens") == "on", now).YAML()
	if err != nil {
		redirect(w, r, "/import", "Making the backup failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="gatusmod-backup-`+now.Format("2006-01-02-150405")+`.yaml"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (a *Admin) backupRestore(w http.ResponseWriter, r *http.Request) {
	data := a.importPage(r)
	defer func() { a.render(w, r, "import.html", data) }()
	file, _, err := r.FormFile("file")
	if err != nil {
		data["RestoreError"] = "Choose a backup file."
		return
	}
	content, err := io.ReadAll(file)
	file.Close()
	if err != nil {
		data["RestoreError"] = "Reading the file failed: " + err.Error()
		return
	}
	b, err := settings.ParseBackup(content)
	if err != nil {
		data["RestoreError"] = err.Error()
		return
	}
	replace := r.PostFormValue("mode") == "replace"
	var res settings.RestoreResult
	err = a.deps.Settings.Update(func(s *settingsT) error {
		res = s.Restore(b, replace)
		return nil
	})
	if err != nil {
		data["RestoreError"] = "Restoring failed, nothing was changed: " + err.Error()
		return
	}
	if len(res.Removed) > 0 {
		removed, err := a.deps.Syncer.RemoveFiles(a.deps.Config(), res.Removed...)
		if err != nil {
			slog.Error("removing the files of deleted imports", "error", err)
		}
		data["RemovedFiles"] = strings.Join(removed, ", ")
	}
	data["Restored"] = res
	data["Replace"] = replace
	for k, v := range a.importPage(r) {
		data[k] = v
	}
}
