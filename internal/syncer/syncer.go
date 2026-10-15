package syncer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gatusmod/internal/config"
	"gatusmod/internal/mapping"
	"gatusmod/internal/store"

	"gopkg.in/yaml.v3"
)

var (
	ErrDisabled       = errors.New("no imports configured")
	ErrUnknownImport  = errors.New("import not found")
	errWaitingForRows = errors.New("not written yet: waiting for the first successful run of")
)

const (
	TriggerStartup   = "startup"
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
	TriggerChange    = "settings changed"

	fetchTimeout = 2 * time.Minute
)

type Status struct {
	Enabled     bool                    `json:"enabled"`
	OutputPath  string                  `json:"outputPath"`
	LastRun     time.Time               `json:"lastRun"`
	LastSuccess time.Time               `json:"lastSuccess"`
	LastWrite   time.Time               `json:"lastWrite"`
	LastError   string                  `json:"lastError"`
	Records     int                     `json:"records"`
	Endpoints   int                     `json:"endpoints"`
	Problems    []Problem               `json:"problems"`
	Imports     map[string]ImportStatus `json:"imports"`
}

// ImportStatus describes the latest run of one import.
type ImportStatus struct {
	LastRun     time.Time     `json:"lastRun"`
	LastSuccess time.Time     `json:"lastSuccess"`
	Trigger     string        `json:"trigger"`
	Duration    time.Duration `json:"duration"`
	Rows        int           `json:"rows"`
	Endpoints   int           `json:"endpoints"`
	Error       string        `json:"error"`
	// File is the generated file the endpoints of the import are written to.
	File string `json:"file"`
}

type cachedRows struct {
	rows      []store.Row
	idField   string
	signature string
}

// Syncer copies the rows of every import into the generated Gatus configuration file, each import on its own schedule.
// The file is only rewritten when its content changes, because every write makes Gatus reload and restart its checks.
type Syncer struct {
	cfg     func() *config.Config
	trigger chan struct{}

	runMu     sync.Mutex
	mu        sync.RWMutex
	status    Status
	cache     map[string]cachedRows
	records   []store.Record
	listeners []func([]store.Record)
}

// OnRecords registers a callback that gets the valid records after every write attempt that built the file.
func (s *Syncer) OnRecords(fn func([]store.Record)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

func (s *Syncer) notify(records []store.Record) {
	s.mu.RLock()
	listeners := append([]func([]store.Record){}, s.listeners...)
	s.mu.RUnlock()
	for _, fn := range listeners {
		fn(append([]store.Record(nil), records...))
	}
}

func New(cfg func() *config.Config) *Syncer {
	return &Syncer{cfg: cfg, trigger: make(chan struct{}, 1), cache: map[string]cachedRows{}, status: Status{Imports: map[string]ImportStatus{}}}
}

func (s *Syncer) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.status
	st.Problems = append([]Problem(nil), s.status.Problems...)
	st.Imports = make(map[string]ImportStatus, len(s.status.Imports))
	for k, v := range s.status.Imports {
		st.Imports[k] = v
	}
	return st
}

// Records returns the valid endpoints of the latest generated file.
func (s *Syncer) Records() []store.Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]store.Record(nil), s.records...)
}

// Trigger applies a configuration change as soon as possible without waiting for it:
// imports whose connection settings changed are fetched again, the others are only mapped again.
func (s *Syncer) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *Syncer) Run(ctx context.Context) {
	s.runAll(ctx, TriggerStartup)
	for {
		cfg := s.cfg()
		now := time.Now()
		due := map[string]time.Time{}
		var next time.Time
		for _, imp := range cfg.Imports {
			t := imp.NextRun(cfg.Defaults.Interval, now)
			if t.IsZero() {
				continue
			}
			due[imp.ID] = t
			if next.IsZero() || t.Before(next) {
				next = t
			}
		}
		var timerC <-chan time.Time
		var timer *time.Timer
		if !next.IsZero() {
			timer = time.NewTimer(time.Until(next))
			timerC = timer.C
		}
		stop := func() {
			if timer != nil {
				timer.Stop()
			}
		}
		select {
		case <-ctx.Done():
			stop()
			return
		case <-s.trigger:
			stop()
			s.refresh(ctx)
		case fired := <-timerC:
			for _, imp := range cfg.Imports {
				if t, ok := due[imp.ID]; ok && !t.After(fired) {
					if err := s.RunImport(ctx, imp.ID, TriggerScheduled); err != nil {
						slog.Error("import failed", "import", imp.Label(), "error", err)
					}
				}
			}
		}
	}
}

// SyncOnce fetches every import and writes the generated file.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	return s.runAll(ctx, TriggerManual)
}

func (s *Syncer) runAll(ctx context.Context, trigger string) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	cfg := s.cfg()
	if len(cfg.Imports) == 0 {
		return s.write(cfg)
	}
	var errs []error
	for _, imp := range cfg.Imports {
		if err := s.fetch(ctx, imp, trigger); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", imp.Label(), err))
		}
	}
	if err := s.write(cfg); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// RunImport fetches one import (and imports that were never read, as the file needs all of them) and writes the generated file.
func (s *Syncer) RunImport(ctx context.Context, id, trigger string) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	cfg := s.cfg()
	var target *config.Import
	for i := range cfg.Imports {
		if cfg.Imports[i].ID == id {
			target = &cfg.Imports[i]
		}
	}
	if target == nil {
		return ErrUnknownImport
	}
	fetchErr := s.fetch(ctx, *target, trigger)
	for _, imp := range cfg.Imports {
		s.mu.RLock()
		_, cached := s.cache[imp.ID]
		s.mu.RUnlock()
		if imp.ID != id && !cached {
			if err := s.fetch(ctx, imp, trigger); err != nil {
				slog.Error("import failed", "import", imp.Label(), "error", err)
			}
		}
	}
	return errors.Join(fetchErr, s.write(cfg))
}

func (s *Syncer) refresh(ctx context.Context) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	cfg := s.cfg()
	for _, imp := range cfg.Imports {
		s.mu.RLock()
		c, ok := s.cache[imp.ID]
		s.mu.RUnlock()
		if !ok || c.signature != signature(imp) {
			if err := s.fetch(ctx, imp, TriggerChange); err != nil {
				slog.Error("import failed", "import", imp.Label(), "error", err)
			}
		}
	}
	if err := s.write(cfg); err != nil && !errors.Is(err, ErrDisabled) {
		slog.Error("writing the generated endpoints failed", "error", err)
	}
}

// signature changes when the rows of an import have to be fetched again.
func signature(imp config.Import) string {
	parts := []string{imp.Provider, imp.URL, imp.Token, imp.TableID, imp.ViewID}
	if imp.Local() {
		data, _ := json.Marshal(struct {
			Columns []string
			Rows    []config.LocalRow
		}{imp.Columns, imp.Rows})
		sum := sha256.Sum256(data)
		parts = append(parts, hex.EncodeToString(sum[:]))
	}
	return strings.Join(parts, "\x00")
}

func (s *Syncer) fetch(ctx context.Context, imp config.Import, trigger string) error {
	start := time.Now()
	client, err := store.NewClient(&imp)
	var rows []store.Row
	if err == nil {
		fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
		rows, err = client.Rows(fetchCtx)
		cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status.Imports[imp.ID]
	st.LastRun = start
	st.Trigger = trigger
	st.Duration = time.Since(start)
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Error = ""
		st.LastSuccess = start
		st.Rows = len(rows)
		s.cache[imp.ID] = cachedRows{rows: rows, idField: client.IDField(), signature: signature(imp)}
	}
	s.status.Imports[imp.ID] = st
	return err
}

// write maps the cached rows of all imports and writes the generated file if it changed.
// It is not written while an import has never been read successfully, so a database outage at startup does not remove its endpoints.
func (s *Syncer) write(cfg *config.Config) error {
	now := time.Now()
	err := s.writeFile(cfg, now)
	s.mu.Lock()
	s.status.Enabled = len(cfg.Imports) > 0
	s.status.LastRun = now
	if err != nil && !errors.Is(err, ErrDisabled) {
		s.status.LastError = err.Error()
	} else {
		s.status.LastError = ""
		s.status.LastSuccess = now
	}
	s.mu.Unlock()
	return err
}

// writeFile writes the local tables to OutputPath and every database import to its own file. An import that was
// never read keeps its file as it is. Files of renamed imports are removed first, so an endpoint is never in two files.
func (s *Syncer) writeFile(cfg *config.Config, now time.Time) error {
	path, err := cfg.OutputPath()
	if err != nil {
		return err
	}
	paths := make(map[string]string, len(cfg.Imports))
	for i := range cfg.Imports {
		p, err := cfg.ImportPath(&cfg.Imports[i])
		if err != nil {
			return err
		}
		paths[cfg.Imports[i].ID] = p
	}
	s.mu.Lock()
	ids := map[string]bool{}
	for _, imp := range cfg.Imports {
		ids[imp.ID] = true
	}
	for id := range s.cache {
		if !ids[id] {
			delete(s.cache, id)
		}
	}
	for id := range s.status.Imports {
		if !ids[id] {
			delete(s.status.Imports, id)
		}
	}
	var all []store.Record
	var problems []Problem
	var waiting []string
	waitingIDs := map[string]bool{}
	rowCount := 0
	for _, imp := range cfg.Imports {
		c, ok := s.cache[imp.ID]
		if !ok {
			waiting = append(waiting, imp.Label())
			waitingIDs[imp.ID] = true
			continue
		}
		rowCount += len(c.rows)
		records, mapProblems := mapping.Records(&imp, c.rows, c.idField)
		for _, p := range mapProblems {
			problems = append(problems, Problem{Record: imp.Label() + ": " + p.Row, Message: p.Message})
		}
		all = append(all, records...)
	}
	s.status.OutputPath = path
	s.mu.Unlock()

	if len(cfg.Imports) == 0 {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			s.mu.Lock()
			s.status.Records, s.status.Endpoints, s.status.Problems, s.records = 0, 0, nil, nil
			s.mu.Unlock()
			s.notify(nil)
			return ErrDisabled
		}
	}
	docs, kept, buildProblems := buildDocs(all, cfg.Defaults.Interval, cfg.GatusToken(), func(r store.Record) string { return paths[r.Source] })
	problems = append(problems, buildProblems...)
	if !hasEndpoints(docs) {
		if docs[path] == nil {
			docs[path] = &doc{list: &yaml.Node{Kind: yaml.SequenceNode}, external: &yaml.Node{Kind: yaml.SequenceNode}}
		}
		docs[path].list.Content = append(docs[path].list.Content, placeholderNode())
	}
	if len(problems) > 0 {
		slog.Warn("skipping invalid rows", "count", len(problems), "first", problems[0].String())
	}

	var errs []error
	changed := false
	if files, err := cfg.GeneratedFiles(); err != nil {
		errs = append(errs, err)
	} else {
		for _, f := range files {
			current, configured := paths[f.ImportID]
			if !configured || waitingIDs[f.ImportID] || sameFile(current, f.Path) {
				continue
			}
			if err := os.Remove(f.Path); err != nil {
				errs = append(errs, err)
				continue
			}
			changed = true
			slog.Info("removed the generated file of a renamed import", "path", f.Path)
		}
	}
	write := func(p, head string) {
		data, err := render(head, docs[p])
		if err == nil {
			var c bool
			c, err = writeIfChanged(p, data)
			changed = changed || c
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if !waitingInShared(cfg, paths, waitingIDs) {
		write(path, header)
	}
	for i := range cfg.Imports {
		imp := &cfg.Imports[i]
		if !imp.Local() && !waitingIDs[imp.ID] {
			write(paths[imp.ID], imp.FileHeader())
		}
	}

	s.mu.Lock()
	s.records = kept
	s.status.Records = rowCount
	s.status.Endpoints = len(kept)
	s.status.Problems = problems
	perImport := map[string]int{}
	for _, r := range kept {
		perImport[r.Source]++
	}
	for _, imp := range cfg.Imports {
		st := s.status.Imports[imp.ID]
		st.Endpoints = perImport[imp.ID]
		st.File = paths[imp.ID]
		s.status.Imports[imp.ID] = st
	}
	if changed {
		s.status.LastWrite = now
	}
	s.mu.Unlock()
	s.notify(kept)
	if changed {
		slog.Info("generated endpoints files updated, Gatus will reload them", "dir", filepath.Dir(path), "endpoints", len(kept))
	}
	if len(waiting) > 0 {
		errs = append(errs, fmt.Errorf("%w %s", errWaitingForRows, strings.Join(waiting, ", ")))
	}
	return errors.Join(errs...)
}

// waitingInShared reports whether a database import that was never read has no file of its own yet: its endpoints
// may still be in the shared file (written before imports had their own files), so that file must be kept.
func waitingInShared(cfg *config.Config, paths map[string]string, waitingIDs map[string]bool) bool {
	for _, imp := range cfg.Imports {
		if imp.Local() || !waitingIDs[imp.ID] {
			continue
		}
		if _, err := os.Stat(paths[imp.ID]); errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func sameFile(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// RemoveFiles deletes the generated files of the given imports. Call it after the imports are removed from the
// settings, so the next sync does not write them again.
func (s *Syncer) RemoveFiles(cfg *config.Config, ids ...string) ([]string, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	files, err := cfg.GeneratedFiles()
	if err != nil {
		return nil, err
	}
	remove := map[string]bool{}
	for _, id := range ids {
		remove[id] = true
	}
	var removed []string
	var errs []error
	for _, f := range files {
		if !remove[f.ImportID] {
			continue
		}
		if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, f.Name)
	}
	return removed, errors.Join(errs...)
}

// OrphanedFiles lists generated files whose import no longer exists.
func OrphanedFiles(cfg *config.Config) []config.GeneratedFile {
	files, err := cfg.GeneratedFiles()
	if err != nil {
		return nil
	}
	ids := map[string]bool{}
	for _, imp := range cfg.Imports {
		ids[imp.ID] = true
	}
	var out []config.GeneratedFile
	for _, f := range files {
		if !ids[f.ImportID] {
			out = append(out, f)
		}
	}
	return out
}

// writeIfChanged writes atomically: a temporary file in the same directory is renamed over the target,
// so Gatus never reads a half-written file. The temporary file has no .yaml extension, so Gatus ignores it.
func writeIfChanged(path string, data []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	time.AfterFunc(touchDelay, func() { touchIfUnchanged(path, data) })
	return true, nil
}

// Gatus compares modification times in whole seconds against the moment it last loaded its configuration,
// so a write in the same second as a (re)load is never noticed. Touching the file again in a later second fixes that.
const touchDelay = 2 * time.Second

func touchIfUnchanged(path string, data []byte) {
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, data) {
		return
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		slog.Warn("cannot update modification time of the generated file", "path", path, "error", err)
	}
}
