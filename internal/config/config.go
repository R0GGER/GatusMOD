// Package config loads the `gatusmod:` block from the Gatus configuration file or directory.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultListen       = ":8080"
	DefaultUpstream     = "http://gatus:8080"
	DefaultImagesDir    = "/images"
	DefaultFaviconDir   = "/favicon"
	DefaultOutputFile   = "zz-gatusmod-endpoints.yaml"
	DefaultSyncInterval = time.Minute
	BaseURLPlaceholder  = "[baseurl]"
	literalDollarSign   = "__GATUSMOD_LITERAL_DOLLAR_SIGN__"
)

var (
	ErrNoConfig          = errors.New("no 'gatusmod:' block found in configuration")
	ErrMultipleConfigs   = errors.New("'gatusmod:' block found in more than one configuration file")
	ErrInvalidProvider   = errors.New("database.provider must be 'nocodb' or 'baserow'")
	ErrMissingTableID    = errors.New("database.table-id is required")
	ErrMissingDBURL      = errors.New("database.url is required")
	ErrInvalidAnalytics  = errors.New("analytics.provider must be 'umami' or 'plausible'")
	ErrMissingAnalytics  = errors.New("analytics.script-url and analytics.site-id are required")
	ErrInvalidAdmin      = errors.New("admin.username and admin.password-bcrypt must both be set")
	ErrNegativePageItems = errors.New("ui.items-per-page must be 0 (Gatus default) or higher")
	ErrInvalidTimeFormat = errors.New("ui.time-format must be empty (browser default), '12h' or '24h'")
	ErrInvalidDateFormat = errors.New("ui.date-format must be empty (browser default), 'DD/MM/YYYY' or 'MM/DD/YYYY'")
)

const (
	TimeFormat12h = "12h"
	TimeFormat24h = "24h"
	DateFormatDMY = "DD/MM/YYYY"
	DateFormatMDY = "MM/DD/YYYY"
)

type Config struct {
	Listen     string     `yaml:"listen,omitempty"`
	Upstream   string     `yaml:"upstream,omitempty"`
	BaseURL    string     `yaml:"base-url,omitempty"`
	ImagesDir  string     `yaml:"images-dir,omitempty"`
	FaviconDir string     `yaml:"favicon-dir,omitempty"`
	PublicDir  string     `yaml:"public-dir,omitempty"`
	Defaults   Defaults   `yaml:"defaults,omitempty"`
	UI         UI         `yaml:"ui,omitempty"`
	Analytics  *Analytics `yaml:"analytics,omitempty"`
	Database   *Database  `yaml:"database,omitempty"`
	Admin      *Admin     `yaml:"admin,omitempty"`
	// OutputFile is the generated endpoints file, relative to the configuration directory or absolute.
	OutputFile string `yaml:"output-file,omitempty"`

	// Imports are the database sources managed on the admin page (stored in the settings file, not in YAML).
	Imports []Import `yaml:"-"`
	// StatusPages (from the settings file) show the endpoints of some groups on their own URL and domains.
	StatusPages []StatusPage `yaml:"-"`
	// Path is the file or directory the configuration was read from.
	Path string `yaml:"-"`
	// ProbeSecret (from the settings file) derives the external endpoint and push tokens.
	ProbeSecret string `yaml:"-"`
}

type Defaults struct {
	Interval string `yaml:"interval,omitempty"`
}

type UI struct {
	ItemsPerPage         int     `yaml:"items-per-page,omitempty"`
	DashboardHeadingShow *bool   `yaml:"dashboard-heading-show,omitempty"`
	SubtitleShow         *bool   `yaml:"subtitle-show,omitempty"`
	Footer               string  `yaml:"footer,omitempty"`
	Favicon              Favicon `yaml:"favicon,omitempty"`
	// TimeFormat and DateFormat change the timestamps of the dashboard; empty keeps the format of the browser.
	TimeFormat string `yaml:"time-format,omitempty"`
	DateFormat string `yaml:"date-format,omitempty"`
}

type Favicon struct {
	Default     string `yaml:"default,omitempty"`
	Size16x16   string `yaml:"size16x16,omitempty"`
	Size32x32   string `yaml:"size32x32,omitempty"`
	AppleTouch  string `yaml:"apple-touch,omitempty"`
	Size192x192 string `yaml:"size192x192,omitempty"`
	Size512x512 string `yaml:"size512x512,omitempty"`
}

type Analytics struct {
	Provider  string `yaml:"provider"`
	ScriptURL string `yaml:"script-url"`
	SiteID    string `yaml:"site-id"`
}

type Database struct {
	Provider     string        `yaml:"provider"`
	URL          string        `yaml:"url"`
	Token        string        `yaml:"token"`
	TableID      string        `yaml:"table-id"`
	SyncInterval time.Duration `yaml:"sync-interval,omitempty"`
	OutputFile   string        `yaml:"output-file,omitempty"`
}

type Admin struct {
	Username       string `yaml:"username"`
	PasswordBcrypt string `yaml:"password-bcrypt"`
}

func (ui *UI) HeadingVisible() bool {
	return ui.DashboardHeadingShow == nil || *ui.DashboardHeadingShow
}
func (ui *UI) SubtitleVisible() bool { return ui.SubtitleShow == nil || *ui.SubtitleShow }

// ResolveURL replaces the [baseurl] placeholder with the configured base URL.
func (cfg *Config) ResolveURL(s string) string {
	return strings.ReplaceAll(s, BaseURLPlaceholder, strings.TrimRight(cfg.BaseURL, "/"))
}

// OutputPath returns the absolute path of the generated endpoints file.
func (cfg *Config) OutputPath() (string, error) {
	file := cfg.OutputFile
	if file == "" {
		file = DefaultOutputFile
	}
	if filepath.IsAbs(file) {
		return file, nil
	}
	info, err := os.Stat(cfg.Path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("configuration path %s is a file: Gatus only reads the generated endpoints when GATUS_CONFIG_PATH is a directory, or set output-file to an absolute path", cfg.Path)
	}
	return filepath.Join(cfg.Path, file), nil
}

// ImportPath returns the generated file of an import: local tables share OutputPath, every database import
// has its own file next to it.
func (cfg *Config) ImportPath(imp *Import) (string, error) {
	out, err := cfg.OutputPath()
	if err != nil || imp.Local() {
		return out, err
	}
	return filepath.Join(filepath.Dir(out), imp.FileName()), nil
}

// GeneratedFile is a file GatusMOD wrote for a database import.
type GeneratedFile struct {
	Path     string
	Name     string
	ImportID string
}

// GeneratedFiles lists the files written for database imports, recognized by their first line, in the directory of OutputPath.
func (cfg *Config) GeneratedFiles() ([]GeneratedFile, error) {
	out, err := cfg.OutputPath()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil {
		return nil, err
	}
	var files []GeneratedFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, generatedFilePrefix) || filepath.Ext(name) != ".yaml" {
			continue
		}
		p := filepath.Join(filepath.Dir(out), name)
		if id, ok := importIDOfFile(p); ok {
			files = append(files, GeneratedFile{Path: p, Name: name, ImportID: id})
		}
	}
	return files, nil
}

func importIDOfFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	return ImportIDFromHeader(line)
}

// validateImportFiles rejects database imports that would write the same file, or the file of the local tables.
func (cfg *Config) validateImportFiles() error {
	reserved := filepath.Base(cfg.OutputFile)
	if reserved == "" || reserved == "." {
		reserved = DefaultOutputFile
	}
	seen := map[string]string{}
	for i := range cfg.Imports {
		imp := &cfg.Imports[i]
		if imp.Local() {
			continue
		}
		name := imp.FileName()
		if strings.EqualFold(name, reserved) {
			return fmt.Errorf("import %q: its file %s is the file of the local tables; choose another name", imp.Label(), name)
		}
		if other, ok := seen[strings.ToLower(name)]; ok {
			return fmt.Errorf("imports %q and %q would both be written to %s; choose another name", other, imp.Label(), name)
		}
		seen[strings.ToLower(name)] = imp.Label()
	}
	return nil
}

// Clone returns a copy that can be modified without changing cfg. Nested pointers are shared, so replace them instead of mutating them.
func (cfg *Config) Clone() *Config {
	c := *cfg
	c.Imports = append([]Import(nil), cfg.Imports...)
	c.StatusPages = CloneStatusPages(cfg.StatusPages)
	return &c
}

// Validate applies defaults and checks the configuration, including the imports.
func (cfg *Config) Validate() error { return cfg.setDefaultsAndValidate() }

func (cfg *Config) setDefaultsAndValidate() error {
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	if cfg.Upstream == "" {
		cfg.Upstream = DefaultUpstream
	}
	if cfg.ImagesDir == "" {
		cfg.ImagesDir = DefaultImagesDir
	}
	if cfg.FaviconDir == "" {
		cfg.FaviconDir = DefaultFaviconDir
	}
	if cfg.Defaults.Interval != "" {
		if d, err := time.ParseDuration(cfg.Defaults.Interval); err != nil || d <= 0 {
			return fmt.Errorf("defaults.interval %q is not a valid positive duration", cfg.Defaults.Interval)
		}
	}
	if cfg.UI.ItemsPerPage < 0 {
		return ErrNegativePageItems
	}
	cfg.UI.TimeFormat = strings.ToLower(cfg.UI.TimeFormat)
	if f := cfg.UI.TimeFormat; f != "" && f != TimeFormat12h && f != TimeFormat24h {
		return ErrInvalidTimeFormat
	}
	cfg.UI.DateFormat = strings.ToUpper(cfg.UI.DateFormat)
	if f := cfg.UI.DateFormat; f != "" && f != DateFormatDMY && f != DateFormatMDY {
		return ErrInvalidDateFormat
	}
	if a := cfg.Analytics; a != nil {
		a.Provider = strings.ToLower(a.Provider)
		if a.Provider != "umami" && a.Provider != "plausible" {
			return ErrInvalidAnalytics
		}
		if a.ScriptURL == "" || a.SiteID == "" {
			return ErrMissingAnalytics
		}
	}
	if db := cfg.Database; db != nil {
		db.Provider = strings.ToLower(db.Provider)
		if db.Provider != "nocodb" && db.Provider != "baserow" {
			return ErrInvalidProvider
		}
		if db.URL == "" {
			return ErrMissingDBURL
		}
		if db.TableID == "" {
			return ErrMissingTableID
		}
		if db.SyncInterval <= 0 {
			db.SyncInterval = DefaultSyncInterval
		}
		if db.OutputFile == "" {
			db.OutputFile = DefaultOutputFile
		}
		if cfg.OutputFile == "" {
			cfg.OutputFile = db.OutputFile
		}
	}
	if cfg.OutputFile == "" {
		cfg.OutputFile = DefaultOutputFile
	}
	if a := cfg.Admin; a != nil && (a.Username == "" || a.PasswordBcrypt == "") {
		return ErrInvalidAdmin
	}
	for i := range cfg.Imports {
		if err := cfg.Imports[i].Validate(); err != nil {
			return fmt.Errorf("import %q: %w", cfg.Imports[i].Label(), err)
		}
	}
	if err := cfg.validateStatusPages(); err != nil {
		return err
	}
	return cfg.validateImportFiles()
}

// Load reads the `gatusmod:` block from a single file or from every YAML file in a directory.
// The generated endpoints file is skipped.
func Load(path string) (*Config, error) {
	files, err := configFiles(path)
	if err != nil {
		return nil, err
	}
	var found *Config
	var foundIn string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var wrapper struct {
			GatusMOD *Config `yaml:"gatusmod"`
		}
		if err := yaml.Unmarshal(expandEnv(data), &wrapper); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", file, err)
		}
		if wrapper.GatusMOD == nil {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%w: %s and %s", ErrMultipleConfigs, foundIn, file)
		}
		found, foundIn = wrapper.GatusMOD, file
	}
	if found == nil {
		return nil, ErrNoConfig
	}
	found.Path = path
	if err := found.setDefaultsAndValidate(); err != nil {
		return nil, fmt.Errorf("%s: %w", foundIn, err)
	}
	return found, nil
}

// LatestModTime returns the most recent modification time of the configuration files, so callers can detect changes.
func LatestModTime(path string) (time.Time, error) {
	files, err := configFiles(path)
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	for _, file := range files {
		if info, err := os.Stat(file); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest, nil
}

func configFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := filepath.Ext(p)
		if (ext != ".yaml" && ext != ".yml") || filepath.Base(p) == DefaultOutputFile {
			return nil
		}
		if strings.HasPrefix(filepath.Base(p), generatedFilePrefix) {
			if _, generated := importIDOfFile(p); generated {
				return nil
			}
		}
		files = append(files, p)
		return nil
	})
	return files, err
}

// ExpandEnv mirrors Gatus: ${VAR} is expanded and $$ is a literal dollar sign.
func ExpandEnv(data []byte) []byte { return expandEnv(data) }

func expandEnv(data []byte) []byte {
	s := strings.ReplaceAll(string(data), "$$", literalDollarSign)
	s = os.ExpandEnv(s)
	return []byte(strings.ReplaceAll(s, literalDollarSign, "$"))
}
