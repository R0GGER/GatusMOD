// Package settings stores what is configured on the admin page and merges it with the `gatusmod:` YAML block.
//
// The settings file contains database tokens, so it is only readable by its owner. It may live next to the Gatus
// configuration, because Gatus and the config watcher only read *.yaml/*.yml files.
package settings

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"gatusmod/internal/config"
)

const FileName = "settings.json"

type Settings struct {
	General General `json:"general"`
	// Databases are the NocoDB and Baserow connections; the imports with a DatabaseID read their tables and views.
	Databases           []config.Connection `json:"databases"`
	Imports             []config.Import     `json:"imports"`
	StatusPages         []config.StatusPage `json:"statusPages,omitempty"`
	AdminPasswordBcrypt string              `json:"adminPasswordBcrypt,omitempty"`
	// MigratedDatabase is set once the `gatusmod.database` YAML block has been turned into an import.
	MigratedDatabase bool `json:"migratedDatabase,omitempty"`
	// ProbeSecret derives the token of the external endpoints and the push URLs. It is generated once;
	// changing it changes every push URL.
	ProbeSecret string `json:"probeSecret,omitempty"`
}

// General overrides the YAML settings; nil means the value from YAML is used.
type General struct {
	BaseURL              *string         `json:"baseUrl,omitempty"`
	DefaultInterval      *string         `json:"defaultInterval,omitempty"`
	ItemsPerPage         *int            `json:"itemsPerPage,omitempty"`
	DashboardHeadingShow *bool           `json:"dashboardHeadingShow,omitempty"`
	SubtitleShow         *bool           `json:"subtitleShow,omitempty"`
	Footer               *string         `json:"footer,omitempty"`
	TimeFormat           *string         `json:"timeFormat,omitempty"`
	DateFormat           *string         `json:"dateFormat,omitempty"`
	Favicon              *config.Favicon `json:"favicon,omitempty"`
	Analytics            *Analytics      `json:"analytics,omitempty"`
}

type Analytics struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider,omitempty"`
	ScriptURL string `json:"scriptUrl,omitempty"`
	SiteID    string `json:"siteId,omitempty"`
}

func (s Settings) clone() Settings {
	s.Databases = append([]config.Connection(nil), s.Databases...)
	s.Imports = append([]config.Import(nil), s.Imports...)
	s.StatusPages = config.CloneStatusPages(s.StatusPages)
	return s
}

// StatusPage returns the status page with the given ID.
func (s Settings) StatusPage(id string) (config.StatusPage, bool) {
	for _, p := range s.StatusPages {
		if p.ID == id {
			return p, true
		}
	}
	return config.StatusPage{}, false
}

// Import returns the import with the given ID, as stored: without the settings of its database.
func (s Settings) Import(id string) (config.Import, bool) {
	for _, imp := range s.Imports {
		if imp.ID == id {
			return imp, true
		}
	}
	return config.Import{}, false
}

// Database returns the database connection with the given ID.
func (s Settings) Database(id string) (config.Connection, bool) {
	for _, c := range s.Databases {
		if c.ID == id {
			return c, true
		}
	}
	return config.Connection{}, false
}

// Views returns the imports that read from the database with the given ID.
func (s Settings) Views(databaseID string) []config.Import {
	var out []config.Import
	for _, imp := range s.Imports {
		if imp.DatabaseID == databaseID {
			out = append(out, imp)
		}
	}
	return out
}

// Resolve fills in the provider, URL, token and database name of an import from its database.
func (s Settings) Resolve(imp config.Import) (config.Import, error) {
	if imp.DatabaseID == "" {
		return imp, nil
	}
	c, ok := s.Database(imp.DatabaseID)
	if !ok {
		return imp, fmt.Errorf("database %q does not exist", imp.DatabaseID)
	}
	imp.Provider, imp.URL, imp.Token, imp.DatabaseName = c.Provider, c.URL, c.Token, c.Name
	return imp, nil
}

// validateDatabases normalizes the connections in place and rejects duplicate names, and duplicate view names per database.
func (s *Settings) validateDatabases() error {
	names := map[string]bool{}
	for i := range s.Databases {
		c := &s.Databases[i]
		if err := c.Validate(); err != nil {
			return fmt.Errorf("database %q: %w", c.Name, err)
		}
		key := strings.ToLower(c.Name)
		if names[key] {
			return fmt.Errorf("there is already a database named %q", c.Name)
		}
		names[key] = true
	}
	views := map[string]bool{}
	for _, imp := range s.Imports {
		if imp.DatabaseID == "" {
			continue
		}
		key := imp.DatabaseID + "\x00" + strings.ToLower(strings.TrimSpace(imp.Name))
		if views[key] {
			c, _ := s.Database(imp.DatabaseID)
			return fmt.Errorf("database %q already has a table or view named %q", c.Name, strings.TrimSpace(imp.Name))
		}
		views[key] = true
	}
	return nil
}

// stripDatabases removes what imports get from their database, so the token is only stored on the database.
func stripDatabases(imps []config.Import) []config.Import {
	out := append([]config.Import(nil), imps...)
	for i := range out {
		if out[i].DatabaseID != "" {
			out[i].Provider, out[i].URL, out[i].Token, out[i].DatabaseName = "", "", "", ""
		}
	}
	return out
}

// Merge applies the settings on top of the YAML configuration and validates the result.
func Merge(base *config.Config, s Settings) (*config.Config, error) {
	cfg := base.Clone()
	g := s.General
	if g.BaseURL != nil {
		cfg.BaseURL = *g.BaseURL
	}
	if g.DefaultInterval != nil {
		cfg.Defaults.Interval = *g.DefaultInterval
	}
	if g.ItemsPerPage != nil {
		cfg.UI.ItemsPerPage = *g.ItemsPerPage
	}
	if g.DashboardHeadingShow != nil {
		v := *g.DashboardHeadingShow
		cfg.UI.DashboardHeadingShow = &v
	}
	if g.SubtitleShow != nil {
		v := *g.SubtitleShow
		cfg.UI.SubtitleShow = &v
	}
	if g.Footer != nil {
		cfg.UI.Footer = *g.Footer
	}
	if g.TimeFormat != nil {
		cfg.UI.TimeFormat = *g.TimeFormat
	}
	if g.DateFormat != nil {
		cfg.UI.DateFormat = *g.DateFormat
	}
	if g.Favicon != nil {
		cfg.UI.Favicon = *g.Favicon
	}
	if a := g.Analytics; a != nil {
		cfg.Analytics = nil
		if a.Enabled {
			cfg.Analytics = &config.Analytics{Provider: a.Provider, ScriptURL: a.ScriptURL, SiteID: a.SiteID}
		}
	}
	if s.AdminPasswordBcrypt != "" && cfg.Admin != nil {
		cfg.Admin = &config.Admin{Username: cfg.Admin.Username, PasswordBcrypt: s.AdminPasswordBcrypt}
	}
	s = s.clone()
	if err := s.validateDatabases(); err != nil {
		return nil, err
	}
	cfg.Imports = make([]config.Import, 0, len(s.Imports))
	for _, imp := range s.Imports {
		resolved, err := s.Resolve(imp)
		if err != nil {
			return nil, fmt.Errorf("import %q: %w", imp.Name, err)
		}
		cfg.Imports = append(cfg.Imports, resolved)
	}
	cfg.StatusPages = config.CloneStatusPages(s.StatusPages)
	cfg.ProbeSecret = s.ProbeSecret
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Manager holds the settings and the effective (merged) configuration.
type Manager struct {
	path      string
	mu        sync.Mutex
	settings  Settings
	base      *config.Config
	merged    atomic.Pointer[config.Config]
	listeners []func(*config.Config)
}

// Open loads the settings file in dir (a missing file means no settings yet) and migrates the legacy database block.
func Open(dir string, base *config.Config) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the data directory: %w", err)
	}
	m := &Manager{path: filepath.Join(dir, FileName), base: base}
	data, err := os.ReadFile(m.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &m.settings); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", m.path, err)
		}
	}
	if err := m.migrate(base); err != nil {
		return nil, err
	}
	if m.settings.ProbeSecret == "" {
		s := m.settings.clone()
		s.ProbeSecret = newSecret()
		if err := m.save(s); err != nil {
			return nil, fmt.Errorf("saving the probe secret: %w", err)
		}
		m.settings = s
	}
	merged, err := Merge(base, m.settings)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.path, err)
	}
	m.merged.Store(merged)
	return m, nil
}

// Path is the location of the settings file.
func (m *Manager) Path() string { return m.path }

// Get returns the effective configuration.
func (m *Manager) Get() *config.Config { return m.merged.Load() }

func (m *Manager) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings.clone()
}

// Base returns the configuration from YAML, without the settings.
func (m *Manager) Base() *config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.base
}

// OnChange registers a callback invoked after the effective configuration changed.
func (m *Manager) OnChange(fn func(*config.Config)) {
	m.mu.Lock()
	m.listeners = append(m.listeners, fn)
	m.mu.Unlock()
}

// Update changes the settings. They are only saved and applied when the merged configuration is valid.
func (m *Manager) Update(fn func(*Settings) error) error {
	m.mu.Lock()
	s := m.settings.clone()
	if err := fn(&s); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := s.validateDatabases(); err != nil {
		m.mu.Unlock()
		return err
	}
	merged, err := Merge(m.base, s)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	s.Imports = stripDatabases(merged.Imports)
	s.StatusPages = config.CloneStatusPages(merged.StatusPages)
	if err := m.save(s); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("saving settings: %w", err)
	}
	m.settings = s
	m.merged.Store(merged)
	listeners := m.snapshotListeners()
	m.mu.Unlock()
	for _, fn := range listeners {
		fn(merged)
	}
	return nil
}

type listener = func(*config.Config)

// snapshotListeners must be called with mu held.
func (m *Manager) snapshotListeners() []listener {
	return append([]listener(nil), m.listeners...)
}

// SetBase applies a reloaded YAML configuration.
func (m *Manager) SetBase(base *config.Config) {
	m.mu.Lock()
	if err := m.migrate(base); err != nil {
		slog.Error("cannot migrate the database block into the settings", "error", err)
	}
	merged, err := Merge(base, m.settings)
	if err != nil {
		m.mu.Unlock()
		slog.Error("the new configuration conflicts with the admin settings, keeping the previous configuration", "error", err)
		return
	}
	m.base = base
	m.merged.Store(merged)
	listeners := m.snapshotListeners()
	m.mu.Unlock()
	for _, fn := range listeners {
		fn(merged)
	}
}

// migrate turns the legacy `gatusmod.database` block into an import once, so it can be managed on the admin page,
// and moves the connection settings of imports that have no database yet into databases.
func (m *Manager) migrate(base *config.Config) error {
	s := m.settings.clone()
	changed := false
	if base.Database != nil && !s.MigratedDatabase {
		imp := config.FromDatabase(base.Database)
		if _, exists := s.Import(imp.ID); exists {
			imp.ID = NewID()
		}
		s.Imports = append(s.Imports, imp)
		s.MigratedDatabase = true
		changed = true
		slog.Info("moved gatusmod.database into the admin settings; manage it on the admin page from now on", "settings", m.path)
	}
	if groupIntoDatabases(&s) {
		changed = true
		slog.Info("moved the connection settings of the imports into databases", "settings", m.path)
	}
	if !changed {
		return nil
	}
	if err := m.save(s); err != nil {
		return err
	}
	m.settings = s
	return nil
}

// groupIntoDatabases gives every database import without a database one, shared by the imports with the same
// provider, URL and token. Databases are named after the provider (NocoDB, NocoDB-2, ...).
func groupIntoDatabases(s *Settings) bool {
	changed := false
	byConn := map[string]string{}
	for i := range s.Imports {
		imp := &s.Imports[i]
		if imp.Local() || imp.DatabaseID != "" {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(imp.Provider))
		key := provider + "\x00" + strings.TrimSpace(imp.URL) + "\x00" + imp.Token
		id, ok := byConn[key]
		if !ok {
			id = NewID()
			byConn[key] = id
			s.Databases = append(s.Databases, config.Connection{
				ID: id, Name: uniqueDatabaseName(*s, config.ProviderTitle(provider)),
				Provider: provider, URL: strings.TrimSpace(imp.URL), Token: imp.Token,
			})
		}
		imp.DatabaseID = id
		imp.Name = uniqueViewName(*s, id, imp.ID, strings.ReplaceAll(strings.TrimSpace(imp.Name), ":", "-"))
		imp.Provider, imp.URL, imp.Token = "", "", ""
		changed = true
	}
	return changed
}

func uniqueDatabaseName(s Settings, name string) string {
	if name == "" {
		name = "database"
	}
	taken := func(n string) bool {
		for _, c := range s.Databases {
			if strings.EqualFold(c.Name, n) {
				return true
			}
		}
		return false
	}
	return uniqueName(name, taken)
}

func uniqueViewName(s Settings, databaseID, importID, name string) string {
	if name == "" {
		name = "table"
	}
	taken := func(n string) bool {
		for _, imp := range s.Imports {
			if imp.ID != importID && imp.DatabaseID == databaseID && strings.EqualFold(strings.TrimSpace(imp.Name), n) {
				return true
			}
		}
		return false
	}
	return uniqueName(name, taken)
}

func uniqueName(name string, taken func(string) bool) string {
	candidate := name
	for n := 2; taken(candidate); n++ {
		candidate = fmt.Sprintf("%s-%d", name, n)
	}
	return candidate
}

// save writes atomically with owner-only permissions, because the file holds database tokens.
func (m *Manager) save(s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
