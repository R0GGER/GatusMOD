// Package importer copies endpoints from an existing Gatus YAML configuration into the central database.
package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gatusmod/internal/store"

	"gopkg.in/yaml.v3"
)

var ErrNoEndpoints = errors.New("no endpoints found: expected a Gatus configuration with an `endpoints:` list")

type Result struct {
	Created   int      `json:"created"`
	Updated   int      `json:"updated"`
	Unchanged int      `json:"unchanged"`
	Problems  []string `json:"problems"`
}

// Parse converts the `endpoints:` list of a Gatus configuration (or a bare list) into records.
// Settings without their own column end up as YAML in the `extra` column.
func Parse(data []byte) ([]store.Record, []string, error) {
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("invalid YAML: %w", err)
	}
	var items []any
	switch v := root.(type) {
	case map[string]any:
		items, _ = v["endpoints"].([]any)
	case []any:
		items = v
	}
	if len(items) == 0 {
		return nil, nil, ErrNoEndpoints
	}
	var records []store.Record
	var problems []string
	seen := map[string]bool{}
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("endpoint #%d is not a mapping, skipped", i+1))
			continue
		}
		r, err := toRecord(m)
		if err != nil {
			problems = append(problems, fmt.Sprintf("endpoint #%d: %v", i+1, err))
			continue
		}
		if seen[r.Key()] {
			problems = append(problems, fmt.Sprintf("endpoint #%d (%s): duplicate group and name, skipped", i+1, r.Name))
			continue
		}
		seen[r.Key()] = true
		records = append(records, r)
	}
	return records, problems, nil
}

func toRecord(m map[string]any) (store.Record, error) {
	take := func(key string) string {
		v, ok := m[key]
		delete(m, key)
		if !ok || v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
	r := store.Record{
		Name:     take("name"),
		Group:    take("group"),
		URL:      take("url"),
		Method:   strings.ToUpper(take("method")),
		Interval: take("interval"),
	}
	if r.Name == "" {
		return r, errors.New("name is required")
	}
	if c, ok := m["conditions"].([]any); ok {
		lines := make([]string, 0, len(c))
		for _, cond := range c {
			lines = append(lines, fmt.Sprint(cond))
		}
		r.Conditions = strings.Join(lines, "\n")
		delete(m, "conditions")
	}
	if enabled, ok := m["enabled"].(bool); ok {
		r.Disabled = !enabled
		delete(m, "enabled")
	}
	if len(m) > 0 {
		extra, err := yaml.Marshal(m)
		if err != nil {
			return r, err
		}
		r.Extra = string(extra)
	}
	return r, nil
}

// Import upserts the endpoints into the store, matching existing rows on group and name.
func Import(ctx context.Context, st store.EndpointStore, data []byte) (Result, error) {
	records, problems, err := Parse(data)
	if err != nil {
		return Result{}, err
	}
	result := Result{Problems: problems}
	existing, err := st.List(ctx)
	if err != nil {
		return result, err
	}
	byKey := make(map[string]store.Record, len(existing))
	for _, r := range existing {
		byKey[r.Key()] = r
	}
	var creates, updates []store.Record
	for _, r := range records {
		current, ok := byKey[r.Key()]
		if !ok {
			creates = append(creates, r)
			continue
		}
		r.ID = current.ID
		if r == current {
			result.Unchanged++
			continue
		}
		updates = append(updates, r)
	}
	if len(creates) > 0 {
		if err := st.Create(ctx, creates); err != nil {
			return result, fmt.Errorf("creating records: %w", err)
		}
		result.Created = len(creates)
	}
	if len(updates) > 0 {
		if err := st.Update(ctx, updates); err != nil {
			return result, fmt.Errorf("updating records: %w", err)
		}
		result.Updated = len(updates)
	}
	return result, nil
}
