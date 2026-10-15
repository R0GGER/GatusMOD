package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kinds of cells in a local endpoint table.
const (
	KindText      = "text"
	KindMultiline = "multiline"
	KindBool      = "bool"
	KindSelect    = "select"
	// KindYAML cells hold a YAML mapping (Shape "map") or list (Shape "list").
	KindYAML = "yaml"
)

// LocalColumn is a Gatus endpoint setting that can be a column of a local table (see https://gatus.io/docs/endpoints).
// A key with a dot, such as dns.query-name, is a nested setting.
type LocalColumn struct {
	Key     string
	Label   string
	Kind    string
	Shape   string
	Options []string
	Group   string
	Help    string
	// Always columns cannot be turned off.
	Always bool
}

// LocalColumns is the catalog in display order. The basic columns are read directly; the others are combined
// into the `extra` YAML of the endpoint.
var LocalColumns = []LocalColumn{
	{Key: "name", Label: "Name", Kind: KindText, Group: "Basic", Always: true, Help: "Name of the endpoint; group + name must be unique."},
	{Key: "group", Label: "Group", Kind: KindText, Group: "Basic", Help: "Empty: the group of the table."},
	{Key: "host", Label: "Host", Kind: KindText, Group: "Basic", Help: "Host name or IP, used as {host} in the URL and name templates."},
	{Key: "port", Label: "Port", Kind: KindText, Group: "Basic", Help: "e.g. 5201, 5201-5210 or 5201, 5202; used as {port} (the first) and {ports}. Empty: the default ports of the table."},
	{Key: "url", Label: "URL", Kind: KindText, Group: "Basic",
		Help: "Empty: the URL template of the table. https://, tcp://, icmp://, dns, ssh://, ... or a GatusMOD check: iperf3://, ntp://, snmp://, smtp://, postgres://, mysql://, redis://, mongodb://, push://"},
	{Key: "method", Label: "Method", Kind: KindSelect, Options: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}, Group: "Basic", Help: "HTTP method. Empty: GET."},
	{Key: "interval", Label: "Interval", Kind: KindText, Group: "Basic", Help: "e.g. 30s, 5m. Empty: the interval of the table."},
	{Key: "conditions", Label: "Conditions", Kind: KindMultiline, Group: "Basic", Help: "One per line. Empty: the conditions of the table."},
	{Key: "enabled", Label: "Status", Kind: KindBool, Group: "Basic", Help: "Status of the endpoint: true (checked) = enabled, false (unchecked) = disabled."},
	{Key: "graphql", Label: "GraphQL", Kind: KindBool, Group: "HTTP", Help: "Send the body as a GraphQL query."},
	{Key: "body", Label: "Body", Kind: KindMultiline, Group: "HTTP", Help: "Request body."},
	{Key: "headers", Label: "Headers", Kind: KindYAML, Shape: "map", Group: "HTTP", Help: "YAML mapping, e.g. Content-Type: application/json"},
	{Key: "dns.query-type", Label: "DNS query type", Kind: KindSelect, Options: []string{"A", "AAAA", "CNAME", "MX", "NS", "PTR", "SRV", "TXT"}, Group: "DNS", Help: "For a DNS endpoint (url: 8.8.8.8)."},
	{Key: "dns.query-name", Label: "DNS query name", Kind: KindText, Group: "DNS", Help: "e.g. example.org"},
	{Key: "ssh.username", Label: "SSH username", Kind: KindText, Group: "SSH", Help: "For an ssh:// endpoint."},
	{Key: "ssh.password", Label: "SSH password", Kind: KindText, Group: "SSH", Help: "Stored in the admin settings file and written to the generated Gatus file."},
	{Key: "client", Label: "Client", Kind: KindYAML, Shape: "map", Group: "Advanced", Help: "YAML mapping, e.g. insecure: true or timeout: 5s"},
	{Key: "alerts", Label: "Alerts", Kind: KindYAML, Shape: "list", Group: "Advanced", Help: "YAML list, e.g. - type: email"},
	{Key: "ui", Label: "UI", Kind: KindYAML, Shape: "map", Group: "Advanced", Help: "YAML mapping, e.g. hide-hostname: true"},
	{Key: "maintenance-windows", Label: "Maintenance windows", Kind: KindYAML, Shape: "list", Group: "Advanced", Help: "YAML list, e.g. - start: \"23:00\""},
	{Key: "extra-labels", Label: "Extra labels", Kind: KindYAML, Shape: "map", Group: "Advanced", Help: "YAML mapping of labels."},
	{Key: "extra", Label: "Extra (YAML)", Kind: KindYAML, Shape: "map", Group: "Advanced", Help: "Any other Gatus setting as a YAML mapping. A dedicated column wins over the same key here."},
}

// DefaultLocalColumns are ticked for a new table.
var DefaultLocalColumns = []string{"name", "group", "host", "interval", "conditions"}

// LocalColumnByKey returns the catalog entry for key.
func LocalColumnByKey(key string) (LocalColumn, bool) {
	for _, c := range LocalColumns {
		if c.Key == key {
			return c, true
		}
	}
	return LocalColumn{}, false
}

// IsLegacyColumn reports whether the column is read directly instead of being combined into the extra YAML.
func IsLegacyColumn(key string) bool {
	switch key {
	case "name", "group", "host", "port", "url", "method", "interval", "conditions", "enabled":
		return true
	}
	return false
}

// ValidateCell checks one cell value of a local table.
func ValidateCell(key, value string) error {
	c, ok := LocalColumnByKey(key)
	if !ok {
		return fmt.Errorf("unknown column %q", key)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	switch c.Kind {
	case KindSelect:
		for _, o := range c.Options {
			if strings.EqualFold(o, value) {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s", strings.Join(c.Options, ", "))
	case KindYAML:
		_, err := ParseYAMLCell(c, value)
		return err
	}
	return nil
}

// ParseYAMLCell parses a YAML cell into a mapping or a list, depending on the column.
func ParseYAMLCell(c LocalColumn, value string) (any, error) {
	if c.Shape == "list" {
		var out []any
		if err := yaml.Unmarshal([]byte(value), &out); err != nil {
			return nil, fmt.Errorf("not a valid YAML list: %v", yamlError(err))
		}
		return out, nil
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(value), &out); err != nil {
		return nil, fmt.Errorf("not a valid YAML mapping: %v", yamlError(err))
	}
	return out, nil
}

func yamlError(err error) string { return strings.TrimPrefix(err.Error(), "yaml: ") }

// NormalizeColumns keeps the known columns in catalog order and always includes the Always columns.
func NormalizeColumns(cols []string) ([]string, error) {
	chosen := map[string]bool{}
	for _, k := range cols {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := LocalColumnByKey(k); !ok {
			return nil, fmt.Errorf("unknown column %q", k)
		}
		chosen[k] = true
	}
	out := make([]string, 0, len(chosen)+2)
	for _, c := range LocalColumns {
		if c.Always || chosen[c.Key] {
			out = append(out, c.Key)
		}
	}
	return out, nil
}
