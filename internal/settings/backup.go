package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"

	"gatusmod/internal/config"
)

// BackupFormat is the version of the backup file; files of a newer version are refused.
const BackupFormat = 1

const backupHeader = "# GatusMOD backup: external databases, their tables and views, local tables and settings.\n" +
	"# Restore it on the Backup & import page of the admin.\n"

// Backup is what a backup file holds: the settings without the admin password.
// It uses the JSON names of the settings file, so both stay in step.
type Backup struct {
	Format      int                 `json:"gatusmodBackup"`
	Exported    string              `json:"exported,omitempty"`
	Tokens      bool                `json:"tokensIncluded"`
	ProbeSecret string              `json:"probeSecret,omitempty"`
	General     General             `json:"general"`
	Databases   []config.Connection `json:"databases"`
	Imports     []config.Import     `json:"imports"`
	StatusPages []config.StatusPage `json:"statusPages,omitempty"`
	// PrivateStatusPages are the slugs of the private status pages whose password was left out.
	PrivateStatusPages []string `json:"privateStatusPages,omitempty"`
}

// NewBackup copies the settings into a backup; without tokens the API tokens of the databases and the password
// hashes of the status pages are left out.
func NewBackup(s Settings, tokens bool, now time.Time) Backup {
	s = s.clone()
	b := Backup{
		Format: BackupFormat, Exported: now.Format(time.RFC3339), Tokens: tokens, ProbeSecret: s.ProbeSecret,
		General: s.General, Databases: s.Databases, Imports: s.Imports, StatusPages: s.StatusPages,
	}
	if b.Databases == nil {
		b.Databases = []config.Connection{}
	}
	if b.Imports == nil {
		b.Imports = []config.Import{}
	}
	if !tokens {
		for i := range b.Databases {
			b.Databases[i].Token = ""
		}
		for i := range b.Imports {
			b.Imports[i].Token = ""
		}
		for i := range b.StatusPages {
			if b.StatusPages[i].Private() {
				b.PrivateStatusPages = append(b.PrivateStatusPages, b.StatusPages[i].Slug)
			}
			b.StatusPages[i].PasswordBcrypt = ""
		}
	}
	return b
}

// YAML writes the backup as a block-style YAML document.
func (b Backup) YAML() ([]byte, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	// JSON is YAML: parsing it keeps the field order of the structs, plainStyle then turns it into block style.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	plainStyle(&doc)
	var buf bytes.Buffer
	buf.WriteString(backupHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func plainStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		plainStyle(c)
	}
}

var ErrNotBackup = errors.New("this is not a GatusMOD backup file (the gatusmodBackup key is missing)")

// ParseBackup reads a backup file written by Backup.YAML.
func ParseBackup(data []byte) (Backup, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Backup{}, fmt.Errorf("the file is not valid YAML: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return Backup{}, ErrNotBackup
	}
	v, err := nodeValue(doc.Content[0])
	if err != nil {
		return Backup{}, err
	}
	data, err = json.Marshal(v)
	if err != nil {
		return Backup{}, err
	}
	var b Backup
	if err := json.Unmarshal(data, &b); err != nil {
		return Backup{}, fmt.Errorf("the backup file is damaged: %w", err)
	}
	switch {
	case b.Format == 0:
		return Backup{}, ErrNotBackup
	case b.Format > BackupFormat:
		return Backup{}, fmt.Errorf("the backup was made by a newer GatusMOD version (format %d); update GatusMOD first", b.Format)
	}
	return b, nil
}

// nodeValue converts YAML into values for encoding/json. Scalars that are not numbers, booleans or null stay
// text, so a cell such as 2026-01-01 is not turned into a timestamp.
func nodeValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.AliasNode:
		return nodeValue(n.Alias)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := nodeValue(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			v, err := nodeValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			out[n.Content[i].Value] = v
		}
		return out, nil
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!null":
			return nil, nil
		case "!!bool", "!!int", "!!float":
			var v any
			if err := n.Decode(&v); err != nil {
				return nil, fmt.Errorf("line %d: %w", n.Line, err)
			}
			return v, nil
		}
		return n.Value, nil
	}
	return nil, nil
}

// RestoreResult counts what a restore changed.
type RestoreResult struct {
	DatabasesAdded, DatabasesUpdated int
	ViewsAdded, ViewsUpdated         int
	TablesAdded, TablesUpdated       int
	// Removed are the IDs of the imports a replace deleted; their generated files must be removed.
	Removed []string
	// MissingTokens are the databases that have no API token after the restore.
	MissingTokens []string
	// LockedStatusPages are private status pages restored without their password: they stay private with an
	// unknown password until the admin sets a new one.
	LockedStatusPages []string
}

// Restore applies a backup. Replace makes the settings exactly the backup; otherwise databases, tables and views
// with the same ID or name are updated and the others added. A database without a token in the backup keeps the
// token it has now, and a status page without a password hash keeps its current password. The admin password is
// never changed, and merging keeps the current probe secret.
func (s *Settings) Restore(b Backup, replace bool) RestoreResult {
	var res RestoreResult
	old := s.clone()
	if replace {
		s.General = b.General
		s.Databases, s.Imports = nil, nil
		if b.ProbeSecret != "" {
			s.ProbeSecret = b.ProbeSecret
		}
	} else {
		s.General = mergeGeneral(s.General, b.General)
	}

	dbIDs := map[string]string{}
	for _, c := range b.Databases {
		backupID := c.ID
		if prev := findDatabase(old.Databases, c); prev >= 0 && c.Token == "" {
			c.Token = old.Databases[prev].Token
		}
		if i := findDatabase(s.Databases, c); i >= 0 {
			c.ID = s.Databases[i].ID
			s.Databases[i] = c
			res.DatabasesUpdated++
		} else {
			if c.ID == "" || findDatabase(s.Databases, config.Connection{ID: c.ID}) >= 0 {
				c.ID = NewID()
			}
			s.Databases = append(s.Databases, c)
			if replace && findDatabase(old.Databases, c) >= 0 {
				res.DatabasesUpdated++
			} else {
				res.DatabasesAdded++
			}
		}
		dbIDs[backupID] = c.ID
		if c.Token == "" {
			res.MissingTokens = append(res.MissingTokens, c.Name)
		}
	}

	for _, imp := range b.Imports {
		if id, ok := dbIDs[imp.DatabaseID]; ok {
			imp.DatabaseID = id
		}
		if imp.DatabaseID != "" {
			imp.Provider, imp.URL, imp.Token = "", "", ""
		}
		existed := findImport(old.Imports, imp) >= 0
		if i := findImport(s.Imports, imp); i >= 0 {
			imp.ID = s.Imports[i].ID
			s.Imports[i] = imp
			existed = true
		} else {
			if imp.ID == "" || findImport(s.Imports, config.Import{ID: imp.ID}) >= 0 {
				imp.ID = NewID()
			}
			s.Imports = append(s.Imports, imp)
		}
		switch {
		case imp.Local() && existed:
			res.TablesUpdated++
		case imp.Local():
			res.TablesAdded++
		case existed:
			res.ViewsUpdated++
		default:
			res.ViewsAdded++
		}
	}

	if replace {
		s.StatusPages = nil
	}
	for _, p := range config.CloneStatusPages(b.StatusPages) {
		if prev := findStatusPage(old.StatusPages, p); prev >= 0 && p.PasswordBcrypt == "" {
			p.PasswordBcrypt = old.StatusPages[prev].PasswordBcrypt
		}
		if p.PasswordBcrypt == "" && slices.Contains(b.PrivateStatusPages, p.Slug) {
			p.PasswordBcrypt = lockedPassword()
			res.LockedStatusPages = append(res.LockedStatusPages, p.Slug)
		}
		if i := findStatusPage(s.StatusPages, p); i >= 0 {
			p.ID = s.StatusPages[i].ID
			s.StatusPages[i] = p
			continue
		}
		if p.ID == "" || findStatusPage(s.StatusPages, config.StatusPage{ID: p.ID}) >= 0 {
			p.ID = NewID()
		}
		s.StatusPages = append(s.StatusPages, p)
	}

	if replace {
		kept := map[string]bool{}
		for _, imp := range s.Imports {
			kept[imp.ID] = true
		}
		for _, imp := range old.Imports {
			if !kept[imp.ID] {
				res.Removed = append(res.Removed, imp.ID)
			}
		}
	}
	return res
}

// findDatabase matches on ID, or else on name.
func findDatabase(dbs []config.Connection, c config.Connection) int {
	for i := range dbs {
		if c.ID != "" && dbs[i].ID == c.ID {
			return i
		}
	}
	name := strings.TrimSpace(c.Name)
	for i := range dbs {
		if name != "" && strings.EqualFold(strings.TrimSpace(dbs[i].Name), name) {
			return i
		}
	}
	return -1
}

// findImport matches on ID, or else on the name within the same database (or among the local tables).
func findImport(imps []config.Import, imp config.Import) int {
	for i := range imps {
		if imp.ID != "" && imps[i].ID == imp.ID {
			return i
		}
	}
	name := strings.TrimSpace(imp.Name)
	for i := range imps {
		if name != "" && imps[i].DatabaseID == imp.DatabaseID && imps[i].Local() == imp.Local() &&
			strings.EqualFold(strings.TrimSpace(imps[i].Name), name) {
			return i
		}
	}
	return -1
}

// lockedPassword is the hash of a random password nobody knows.
func lockedPassword() string {
	hash, err := bcrypt.GenerateFromPassword([]byte(newSecret()), bcrypt.DefaultCost)
	if err != nil {
		return "!"
	}
	return string(hash)
}

// findStatusPage matches on ID, or else on slug.
func findStatusPage(pages []config.StatusPage, p config.StatusPage) int {
	for i := range pages {
		if p.ID != "" && pages[i].ID == p.ID {
			return i
		}
	}
	for i := range pages {
		if p.Slug != "" && strings.EqualFold(pages[i].Slug, strings.TrimSpace(p.Slug)) {
			return i
		}
	}
	return -1
}

// mergeGeneral takes every setting the backup sets, and keeps the others.
func mergeGeneral(cur, b General) General {
	if b.BaseURL != nil {
		cur.BaseURL = b.BaseURL
	}
	if b.DefaultInterval != nil {
		cur.DefaultInterval = b.DefaultInterval
	}
	if b.ItemsPerPage != nil {
		cur.ItemsPerPage = b.ItemsPerPage
	}
	if b.DashboardHeadingShow != nil {
		cur.DashboardHeadingShow = b.DashboardHeadingShow
	}
	if b.SubtitleShow != nil {
		cur.SubtitleShow = b.SubtitleShow
	}
	if b.Footer != nil {
		cur.Footer = b.Footer
	}
	if b.TimeFormat != nil {
		cur.TimeFormat = b.TimeFormat
	}
	if b.DateFormat != nil {
		cur.DateFormat = b.DateFormat
	}
	if b.Logo != nil {
		cur.Logo = b.Logo
	}
	if b.LogoLink != nil {
		cur.LogoLink = b.LogoLink
	}
	if b.Favicon != nil {
		cur.Favicon = b.Favicon
	}
	if b.Analytics != nil {
		cur.Analytics = b.Analytics
	}
	return cur
}
