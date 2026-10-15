package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	TypeHTTP   = "http"
	TypeTCP    = "tcp"
	TypeICMP   = "icmp"
	TypeCustom = "custom"

	// Types checked by GatusMOD itself and reported to Gatus as external endpoints.
	TypeIPerf3   = "iperf3"
	TypeNTP      = "ntp"
	TypeSNMP     = "snmp"
	TypeSMTP     = "smtp"
	TypeDatabase = "database"
	TypePush     = "push"

	// FallbackInterval is used for the schedule when neither the import nor defaults.interval set an interval.
	FallbackInterval = time.Minute

	// ProviderLocal is a table edited on the admin page; its rows are stored in the settings file.
	ProviderLocal = "local"
)

var (
	ImportTypes = []string{TypeHTTP, TypeTCP, TypeICMP, TypeIPerf3, TypeNTP, TypeSNMP, TypeSMTP, TypeDatabase, TypePush, TypeCustom}
	Providers   = []string{"baserow", "nocodb"}

	ErrMissingImportName  = errors.New("name is required")
	ErrInvalidImportType  = errors.New("type must be one of " + strings.Join(ImportTypes, ", "))
	ErrCustomNeedsURL     = errors.New("type custom and database need a column for url or a URL template")
	ErrMissingImportTable = errors.New("table ID is required. NocoDB: open the table and use Details > API snippets " +
		"(the ID starts with m), or paste the table URL. Baserow: the number after /table/ in the table URL, or paste the URL")

	baserowTableRef = regexp.MustCompile(`/table/(\d+)(?:/(\d+))?`)
	nocodbTableRef  = regexp.MustCompile(`(?:^|[/=?&#])(m[a-z0-9]{8,})(?:$|[/?&#])`)
	nocodbViewRef   = regexp.MustCompile(`(?:^|[/=?&#])(vw[a-z0-9]{8,})(?:$|[/?&#])`)
)

// splitTableRef accepts a table URL instead of an ID: the table ID (and view ID, if any) are taken from it.
func splitTableRef(provider, ref string) (table, view string) {
	if !strings.ContainsAny(ref, "/#?") {
		return ref, ""
	}
	path := ref
	if i := strings.Index(path, "://"); i >= 0 {
		path = path[i+3:]
		if j := strings.IndexAny(path, "/#?"); j >= 0 {
			path = path[j:]
		}
	}
	switch provider {
	case "baserow":
		if m := baserowTableRef.FindStringSubmatch(path); m != nil {
			return m[1], m[2]
		}
	case "nocodb":
		if m := nocodbTableRef.FindStringSubmatch(path); m != nil {
			table = m[1]
			if v := nocodbViewRef.FindStringSubmatch(path); v != nil {
				view = v[1]
			}
			return table, view
		}
	}
	return ref, ""
}

// Connection is a NocoDB or Baserow database the tables and views of imports are read from.
type Connection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Token    string `json:"token,omitempty"`
}

var ErrMissingDatabaseName = errors.New("database name is required")

// ProviderTitle is how a provider is written in names, e.g. NocoDB.
func ProviderTitle(provider string) string {
	switch provider {
	case "nocodb":
		return "NocoDB"
	case "baserow":
		return "Baserow"
	}
	return provider
}

// Validate normalizes the connection and reports the first problem.
func (c *Connection) Validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.URL = strings.TrimSpace(c.URL)
	if c.Name == "" {
		return ErrMissingDatabaseName
	}
	if strings.Contains(c.Name, ":") {
		return fmt.Errorf("database name %q must not contain ':'", c.Name)
	}
	if c.Provider != "nocodb" && c.Provider != "baserow" {
		return ErrInvalidProvider
	}
	if c.URL == "" {
		return ErrMissingDBURL
	}
	return nil
}

// Import is one database source: the rows of a NocoDB or Baserow table (or view) become Gatus endpoints.
type Import struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// DatabaseID links the import to a Connection; Provider, URL, Token and DatabaseName are then filled in
	// from it when the settings are merged, and not stored on the import.
	DatabaseID   string `json:"databaseId,omitempty"`
	DatabaseName string `json:"-"`

	Provider string `json:"provider"`
	Group    string `json:"group,omitempty"`
	URL      string `json:"url"`
	Token    string `json:"token,omitempty"`
	TableID  string `json:"tableId"`
	ViewID   string `json:"viewId,omitempty"`

	Type         string `json:"type"`
	Fields       Fields `json:"fields"`
	URLTemplate  string `json:"urlTemplate,omitempty"`
	NameTemplate string `json:"nameTemplate,omitempty"`
	DefaultPorts string `json:"defaultPorts,omitempty"`
	Conditions   string `json:"conditions,omitempty"`
	Interval     string `json:"interval,omitempty"`

	// ManualSchedule uses Cron instead of following the interval; an empty Cron means the import only runs on demand.
	ManualSchedule bool   `json:"manualSchedule,omitempty"`
	Cron           string `json:"cron,omitempty"`

	// Columns and Rows are the chosen catalog columns and the rows of a local table.
	Columns []string   `json:"columns,omitempty"`
	Rows    []LocalRow `json:"rows,omitempty"`
}

// LocalRow is one row of a local table: column key to cell text.
type LocalRow map[string]string

// Local reports whether the rows are edited on the admin page instead of read from NocoDB or Baserow.
func (imp *Import) Local() bool { return imp.Provider == ProviderLocal }

// Label is the name shown for the import: database:name, e.g. NocoDB:http-sites.
func (imp *Import) Label() string {
	if imp.DatabaseName == "" || imp.Local() {
		return imp.Name
	}
	return imp.DatabaseName + ":" + imp.Name
}

const (
	generatedFilePrefix = "zz-gatusmod-"
	importHeaderPrefix  = "# Generated by GatusMOD for "
)

var (
	slugInvalid  = regexp.MustCompile(`[^a-z0-9._-]+`)
	slugDashes   = regexp.MustCompile(`-{2,}`)
	importHeader = regexp.MustCompile(`^# Generated by GatusMOD for .* \(import ([0-9A-Za-z_-]+)\)\.`)
)

// Slug turns a label into a file name part: NocoDB-prod:http-sites becomes nocodb-prod-http-sites.
func Slug(s string) string {
	s = slugInvalid.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(slugDashes.ReplaceAllString(s, "-"), "-.")
}

// FileName is the generated file of a database import, e.g. zz-gatusmod-nocodb-prod-http-sites.yaml.
func (imp *Import) FileName() string {
	slug := Slug(imp.Label())
	if slug == "" {
		slug = Slug(imp.ID)
	}
	return generatedFilePrefix + slug + ".yaml"
}

// FileHeader is the first line of the generated file of a database import; ImportIDFromHeader reads it back.
func (imp *Import) FileHeader() string {
	label := strings.NewReplacer("\n", " ", "\r", " ").Replace(imp.Label())
	return importHeaderPrefix + label + " (import " + imp.ID + "). Do not edit: changes are overwritten on the next sync.\n"
}

// ImportIDFromHeader returns the import ID of a file written by GatusMOD for a database import.
func ImportIDFromHeader(firstLine string) (string, bool) {
	m := importHeader.FindStringSubmatch(firstLine)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Fields maps Gatus endpoint settings to column names. An empty name means the column is not used.
type Fields struct {
	Name       string `json:"name,omitempty"`
	Group      string `json:"group,omitempty"`
	URL        string `json:"url,omitempty"`
	Host       string `json:"host,omitempty"`
	Port       string `json:"port,omitempty"`
	Method     string `json:"method,omitempty"`
	Interval   string `json:"interval,omitempty"`
	Conditions string `json:"conditions,omitempty"`
	// Enabled is a checkbox column: true = enabled, false = disabled.
	Enabled string `json:"enabled,omitempty"`
	// Disabled is the inverse of Enabled (true = disabled), for the `gatusmod.database` table and views mapped
	// before Enabled existed. It is only used when Enabled is empty.
	Disabled string `json:"disabled,omitempty"`
	Extra    string `json:"extra,omitempty"`
}

// LegacyFields are the fixed column names of the `gatusmod.database` table.
var LegacyFields = Fields{
	Name: "name", Group: "group", URL: "url", Method: "method", Interval: "interval",
	Conditions: "conditions", Disabled: "disabled", Extra: "extra",
}

// LocalFields are the fixed column names of a local table.
var LocalFields = Fields{
	Name: "name", Group: "group", URL: "url", Host: "host", Port: "port", Method: "method", Interval: "interval",
	Conditions: "conditions", Enabled: "enabled", Extra: "extra",
}

// DefaultURLTemplate is used when the import has no URL template and the row has no url column value.
func DefaultURLTemplate(typ string) string {
	switch typ {
	case TypeHTTP:
		return "https://{host}"
	case TypeTCP:
		return "tcp://{host}:{port}"
	case TypeICMP:
		return "icmp://{host}"
	case TypeIPerf3:
		return "iperf3://{host}:{port}"
	case TypeNTP:
		return "ntp://{host}:{port}"
	case TypeSNMP:
		return "snmp://public@{host}:{port}"
	case TypeSMTP:
		return "smtp://{host}:{port}"
	case TypePush:
		return "push://"
	}
	return ""
}

// URLExample is a URL template the import form offers for a primary check.
type URLExample struct {
	Label    string
	Template string
}

// URLTemplateExamples lists example URL templates per primary check.
func URLTemplateExamples(typ string) []URLExample {
	switch typ {
	case TypeHTTP:
		return []URLExample{
			{"Home page", "https://{host}"},
			{"Health endpoint", "https://{host}/health"},
			{"Plain HTTP on a port", "http://{host}:{port}"},
		}
	case TypeTCP:
		return []URLExample{{"Port open", "tcp://{host}:{port}"}}
	case TypeICMP:
		return []URLExample{{"Ping", "icmp://{host}"}}
	case TypeIPerf3:
		return []URLExample{
			{"Handshake only, no data", "iperf3://{host}:{port}?ports={ports}"},
			{"Upload speed", "iperf3://{host}:{port}?ports={ports}&bandwidth=3s"},
			{"Download speed", "iperf3://{host}:{port}?ports={ports}&bandwidth=3s&reverse=true"},
			{"Upload test passes, at most 1 Mbit/s over IPv4", "iperf3://{host}:{port}?ports={ports}&bandwidth=3s&rate=1M&ip=4"},
			{"Download test passes, at most 1 Mbit/s over IPv4", "iperf3://{host}:{port}?ports={ports}&bandwidth=3s&rate=1M&ip=4&reverse=true"},
		}
	case TypeNTP:
		return []URLExample{
			{"Time server answers", "ntp://{host}:{port}"},
			{"Clock offset at most 500ms", "ntp://{host}:{port}?max-offset=500ms"},
		}
	case TypeSNMP:
		return []URLExample{
			{"Uptime (sysUpTime)", "snmp://public@{host}:{port}"},
			{"System name (sysName)", "snmp://public@{host}:{port}/1.3.6.1.2.1.1.5.0"},
		}
	case TypeSMTP:
		return []URLExample{
			{"SMTP on port 25", "smtp://{host}:25"},
			{"Submission with STARTTLS", "smtp://{host}:587?starttls=true"},
			{"SMTPS (TLS)", "smtps://{host}:465"},
		}
	case TypeDatabase:
		return []URLExample{
			{"PostgreSQL", "postgres://monitor:${DB_PASSWORD}@{host}:5432/postgres?sslmode=disable"},
			{"MySQL / MariaDB", "mysql://monitor:${DB_PASSWORD}@{host}:3306/mysql"},
			{"Redis", "redis://:${REDIS_PASSWORD}@{host}:6379/0"},
			{"MongoDB", "mongodb://monitor:${DB_PASSWORD}@{host}:27017/admin"},
		}
	}
	return nil
}

// DefaultConditions are used when neither the row nor the import has conditions. Push endpoints have none:
// the pushed status is the result.
func DefaultConditions(typ string) string {
	switch typ {
	case TypeTCP, TypeICMP, TypeIPerf3, TypeNTP, TypeSNMP, TypeDatabase:
		return "[CONNECTED] == true"
	case TypeSMTP:
		return "[CONNECTED] == true\n[CODE] == 220"
	case TypePush:
		return ""
	}
	return "[STATUS] == 200"
}

// FromDatabase converts the legacy `gatusmod.database` block into an import with the fixed column names.
func FromDatabase(db *Database) Import {
	imp := Import{
		ID: "yaml", Name: "database", Provider: db.Provider, URL: db.URL, Token: db.Token, TableID: db.TableID,
		Type: TypeCustom, Fields: LegacyFields,
	}
	if db.SyncInterval > 0 {
		imp.ManualSchedule = true
		imp.Cron = "@every " + db.SyncInterval.String()
	}
	return imp
}

func (imp *Import) normalize() {
	imp.Name = strings.TrimSpace(imp.Name)
	imp.Group = strings.TrimSpace(imp.Group)
	imp.Provider = strings.ToLower(strings.TrimSpace(imp.Provider))
	imp.URL = strings.TrimSpace(imp.URL)
	imp.TableID = strings.TrimSpace(imp.TableID)
	imp.ViewID = strings.TrimSpace(imp.ViewID)
	if table, view := splitTableRef(imp.Provider, imp.TableID); table != imp.TableID {
		imp.TableID = table
		if imp.ViewID == "" {
			imp.ViewID = view
		}
	}
	imp.Type = strings.ToLower(strings.TrimSpace(imp.Type))
	if imp.Type == "" {
		imp.Type = TypeCustom
	}
	imp.Interval = strings.TrimSpace(imp.Interval)
	imp.Cron = strings.TrimSpace(imp.Cron)
	imp.DefaultPorts = strings.TrimSpace(imp.DefaultPorts)
}

// Validate normalizes the import and reports the first problem.
func (imp *Import) Validate() error {
	imp.normalize()
	if imp.Name == "" {
		return ErrMissingImportName
	}
	if imp.DatabaseID != "" && strings.Contains(imp.Name, ":") {
		return fmt.Errorf("name %q must not contain ':'", imp.Name)
	}
	if imp.Local() {
		return imp.validateLocal()
	}
	if imp.Provider != "nocodb" && imp.Provider != "baserow" {
		return ErrInvalidProvider
	}
	if imp.URL == "" {
		return ErrMissingDBURL
	}
	if imp.TableID == "" {
		return ErrMissingImportTable
	}
	if err := imp.validateType(imp.Fields.URL != ""); err != nil {
		return err
	}
	if err := imp.validateInterval(); err != nil {
		return err
	}
	if imp.ManualSchedule && imp.Cron != "" {
		if _, err := ParseCron(imp.Cron); err != nil {
			return fmt.Errorf("cron %q: %w", imp.Cron, err)
		}
	}
	return nil
}

// validateType checks the primary check; hasURLColumn reports whether rows can bring their own URL.
func (imp *Import) validateType(hasURLColumn bool) error {
	if !slices.Contains(ImportTypes, imp.Type) {
		return ErrInvalidImportType
	}
	if (imp.Type == TypeCustom || imp.Type == TypeDatabase) && !hasURLColumn && imp.URLTemplate == "" {
		return ErrCustomNeedsURL
	}
	return nil
}

func (imp *Import) validateInterval() error {
	if imp.Interval != "" {
		if d, err := time.ParseDuration(imp.Interval); err != nil || d <= 0 {
			return fmt.Errorf("interval %q is not a valid positive duration (examples: 30s, 5m, 1h)", imp.Interval)
		}
	}
	return nil
}

// validateLocal fixes the settings that do not apply to a local table: its columns have fixed names
// and it is re-read whenever it is saved, not on a schedule.
func (imp *Import) validateLocal() error {
	imp.URL, imp.Token, imp.TableID, imp.ViewID = "", "", "", ""
	imp.Fields = LocalFields
	imp.URLTemplate = strings.TrimSpace(imp.URLTemplate)
	imp.NameTemplate = strings.TrimSpace(imp.NameTemplate)
	imp.ManualSchedule, imp.Cron = false, ""
	// Tables saved before url became optional may have no column list; their rows still carry a url.
	if imp.Type == TypeCustom && len(imp.Columns) == 0 {
		imp.Columns = []string{"url"}
	}
	imp.migrateDisabledColumn()
	cols, err := NormalizeColumns(imp.Columns)
	if err != nil {
		return err
	}
	imp.Columns = cols
	if err := imp.validateType(slices.Contains(cols, "url")); err != nil {
		return err
	}
	return imp.validateInterval()
}

// migrateDisabledColumn turns the disabled column of tables saved before it became the enabled column into an
// enabled column with the inverse values. Columns and rows are copied, as they are shared with the stored settings.
func (imp *Import) migrateDisabledColumn() {
	i := slices.Index(imp.Columns, "disabled")
	if i < 0 {
		return
	}
	imp.Columns = slices.Clone(imp.Columns)
	imp.Columns[i] = "enabled"
	rows := make([]LocalRow, len(imp.Rows))
	for n, lr := range imp.Rows {
		row := LocalRow{}
		for k, v := range lr {
			row[k] = v
		}
		off, _ := strconv.ParseBool(strings.TrimSpace(row["disabled"]))
		delete(row, "disabled")
		row["enabled"] = strconv.FormatBool(!off)
		rows[n] = row
	}
	imp.Rows = rows
}

// ParseCron accepts standard 5-field cron expressions and descriptors such as @hourly or @every 10m.
func ParseCron(expr string) (cron.Schedule, error) {
	return cron.ParseStandard(expr)
}

// ScheduleInterval is the interval the import follows when the schedule is not set manually.
func (imp *Import) ScheduleInterval(defaultInterval string) time.Duration {
	for _, s := range []string{imp.Interval, defaultInterval} {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	return FallbackInterval
}

// NextRun returns when the import runs next after now; the zero time means it only runs on demand.
// Interval schedules are aligned to the clock, e.g. :00, :15, :30 and :45 for 15m.
func (imp *Import) NextRun(defaultInterval string, now time.Time) time.Time {
	if imp.Local() {
		return time.Time{}
	}
	if imp.ManualSchedule {
		if imp.Cron == "" {
			return time.Time{}
		}
		sched, err := ParseCron(imp.Cron)
		if err != nil {
			return time.Time{}
		}
		return sched.Next(now)
	}
	d := imp.ScheduleInterval(defaultInterval)
	_, offset := now.Zone()
	shift := time.Duration(offset) * time.Second
	return now.Add(shift).Truncate(d).Add(d).Add(-shift)
}

// ScheduleText describes the schedule for the admin page.
func (imp *Import) ScheduleText(defaultInterval string) string {
	if imp.Local() {
		return "On save"
	}
	if imp.ManualSchedule {
		if imp.Cron == "" {
			return "Manual only"
		}
		return "Cron " + imp.Cron
	}
	return "Every " + shortDuration(imp.ScheduleInterval(defaultInterval))
}

func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}
