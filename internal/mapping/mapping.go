// Package mapping turns table rows into Gatus endpoint records using the field mapping and templates of an import.
package mapping

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gatusmod/internal/config"
	"gatusmod/internal/store"
)

type Problem struct {
	Row     string
	Message string
}

var placeholder = regexp.MustCompile(`\{([^{}]+)\}`)

// Records maps every row; rows that cannot become an endpoint are reported instead.
func Records(imp *config.Import, rows []store.Row, idField string) ([]store.Record, []Problem) {
	records := make([]store.Record, 0, len(rows))
	var problems []Problem
	for i, row := range rows {
		r, err := Record(imp, row, idField)
		if err != nil {
			label := "row " + strconv.Itoa(i+1)
			if id := store.Text(row[idField]); id != "" {
				label = "row id " + id
			}
			problems = append(problems, Problem{Row: label, Message: err.Error()})
			continue
		}
		records = append(records, r)
	}
	return records, problems
}

// Record maps one row. A mapped column with a value wins over the template or import default.
func Record(imp *config.Import, row store.Row, idField string) (store.Record, error) {
	f := imp.Fields
	r := store.RecordFromRow(row, f, idField)
	r.Source, r.SourceName = imp.ID, imp.Label()
	if r.Group == "" {
		r.Group = imp.Group
	}
	host := column(row, f.Host)
	ports := column(row, f.Port)
	port := firstPort(ports)
	if port == "" {
		ports = imp.DefaultPorts
		port = firstPort(ports)
	}
	if port == "" {
		port = config.DefaultPort(imp.Type)
		ports = port
	}
	ports = strings.Join(strings.Fields(ports), "")
	values := func(key string) (string, bool) {
		switch key {
		case "host":
			return host, true
		case "port":
			return port, true
		case "ports":
			return ports, true
		case "id":
			return r.ID, true
		case "group":
			return r.Group, true
		}
		if v, ok := row[key]; ok {
			return strings.TrimSpace(store.Text(v)), true
		}
		return "", false
	}
	if r.Name == "" {
		tmpl := imp.NameTemplate
		if tmpl == "" && f.Host != "" {
			tmpl = "{host}"
		}
		if tmpl != "" {
			name, err := render(tmpl, values)
			if err != nil {
				return r, fmt.Errorf("name: %w", err)
			}
			r.Name = name
		}
	}
	if r.Name == "" {
		return r, errors.New("no name: map a name column or set a name template")
	}
	if r.URL == "" {
		tmpl := imp.URLTemplate
		if tmpl == "" {
			tmpl = config.DefaultURLTemplate(imp.Type)
			// The iperf3 probe tries the next port of a range while a server port is busy.
			if imp.Type == config.TypeIPerf3 && ports != port {
				tmpl += "?ports={ports}"
			}
		}
		if tmpl == "" {
			return r, errors.New("url is empty")
		}
		u, err := render(tmpl, values)
		if err != nil {
			return r, fmt.Errorf("url: %w", err)
		}
		r.URL = u
	}
	if r.Interval == "" {
		r.Interval = imp.Interval
	}
	if strings.TrimSpace(r.Conditions) == "" && !strings.Contains(r.Extra, "conditions:") {
		r.Conditions = imp.Conditions
		if strings.TrimSpace(r.Conditions) == "" {
			typ := imp.Type
			if t := config.SchemeType(r.URL); t != "" {
				typ = t
			}
			r.Conditions = config.DefaultConditions(typ)
		}
	}
	return r, nil
}

// render replaces {key} placeholders: host, port, ports, id, group or any column name.
func render(tmpl string, values func(string) (string, bool)) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		key := m[1 : len(m)-1]
		v, ok := values(key)
		if !ok || v == "" {
			missing = append(missing, key)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("no value for {%s}", strings.Join(missing, "}, {"))
	}
	return strings.TrimSpace(out), nil
}

func column(row store.Row, col string) string {
	if col == "" {
		return ""
	}
	return strings.TrimSpace(store.Text(row[col]))
}

// firstPort returns the first port of values like "5201", "5201-5210" or "5201, 5202".
func firstPort(s string) string {
	s = strings.TrimSpace(s)
	end := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if end >= 0 {
		s = s[:end]
	}
	if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
		return ""
	}
	return s
}
