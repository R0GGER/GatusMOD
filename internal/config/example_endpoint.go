package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// exampleEndpointPath is the URL path the example endpoint of config.yaml checks: GatusMOD itself.
const exampleEndpointPath = "/mod/health"

var (
	ErrNoExampleEndpoint = errors.New("the configuration has no example endpoint")
	ErrLastEndpoint      = errors.New("the example endpoint is the only endpoint and Gatus does not start without one: wait until your tables have generated endpoints")
)

// ExampleEndpoint is the endpoint in a configuration file that checks the health URL of GatusMOD.
type ExampleEndpoint struct {
	File, Name, Group string
	// OtherEndpoints counts the endpoints Gatus keeps without this one, in all configuration and generated files.
	OtherEndpoints int
}

// Label is the name of the endpoint as the dashboard shows it.
func (e *ExampleEndpoint) Label() string {
	if e.Group == "" {
		return e.Name
	}
	return e.Group + "/" + e.Name
}

// exampleLocation is where the example endpoint is in a parsed file.
type exampleLocation struct {
	root     *yaml.Node
	keyIndex int // index of the `endpoints` key in root.Content
	item     int // index of the example in the sequence
	name     string
	group    string
}

func (l exampleLocation) items() []*yaml.Node { return l.root.Content[l.keyIndex+1].Content }

// FindExampleEndpoint returns the example endpoint in the configuration at path, or nil when there is none.
func FindExampleEndpoint(path string) (*ExampleEndpoint, error) {
	files, err := configFiles(path)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		loc, ok := findExample(data)
		if !ok {
			continue
		}
		total, err := countEndpoints(path)
		if err != nil {
			return nil, err
		}
		return &ExampleEndpoint{File: file, Name: loc.name, Group: loc.group, OtherEndpoints: total - 1}, nil
	}
	return nil, nil
}

// RemoveExampleEndpoint removes the example endpoint from its file and keeps the rest of the file as it is.
// It refuses when Gatus would be left without endpoints.
func RemoveExampleEndpoint(path string) (*ExampleEndpoint, error) {
	ex, err := FindExampleEndpoint(path)
	if err != nil {
		return nil, err
	}
	if ex == nil {
		return nil, ErrNoExampleEndpoint
	}
	if ex.OtherEndpoints < 1 {
		return ex, ErrLastEndpoint
	}
	data, err := os.ReadFile(ex.File)
	if err != nil {
		return ex, err
	}
	loc, ok := findExample(data)
	if !ok {
		return ex, ErrNoExampleEndpoint
	}
	out, err := removeExample(data, loc)
	if err != nil {
		return ex, fmt.Errorf("%s: %w; remove the endpoint by hand", filepath.Base(ex.File), err)
	}
	info, err := os.Stat(ex.File)
	if err != nil {
		return ex, err
	}
	// Gatus watches the directory, so it must never see a half-written .yaml file.
	tmp := filepath.Join(filepath.Dir(ex.File), "."+filepath.Base(ex.File)+".tmp")
	if err := os.WriteFile(tmp, out, info.Mode().Perm()); err != nil {
		return ex, err
	}
	if err := os.Rename(tmp, ex.File); err != nil {
		_ = os.Remove(tmp)
		return ex, err
	}
	return ex, nil
}

func parseRoot(data []byte) *yaml.Node {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	if root := doc.Content[0]; root.Kind == yaml.MappingNode {
		return root
	}
	return nil
}

// endpointsIndex returns the index of the `endpoints` key in a mapping, or -1.
func endpointsIndex(root *yaml.Node) int {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "endpoints" && root.Content[i+1].Kind == yaml.SequenceNode {
			return i
		}
	}
	return -1
}

func findExample(data []byte) (exampleLocation, bool) {
	root := parseRoot(data)
	if root == nil {
		return exampleLocation{}, false
	}
	k := endpointsIndex(root)
	if k < 0 {
		return exampleLocation{}, false
	}
	for i, item := range root.Content[k+1].Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		values := map[string]string{}
		for j := 0; j+1 < len(item.Content); j += 2 {
			values[item.Content[j].Value] = item.Content[j+1].Value
		}
		if u, err := url.Parse(strings.TrimSpace(values["url"])); err == nil && u.Path == exampleEndpointPath {
			return exampleLocation{root: root, keyIndex: k, item: i, name: values["name"], group: values["group"]}, true
		}
	}
	return exampleLocation{}, false
}

// countEndpoints counts the regular endpoints in every YAML file Gatus reads, generated files included.
// External endpoints are not counted: Gatus does not start with only those.
func countEndpoints(path string) (int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	files := []string{path}
	if info.IsDir() {
		files = nil
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (filepath.Ext(p) == ".yaml" || filepath.Ext(p) == ".yml") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	total := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return 0, err
		}
		if root := parseRoot(data); root != nil {
			if k := endpointsIndex(root); k >= 0 {
				total += len(root.Content[k+1].Content)
			}
		}
	}
	return total, nil
}

// removeExample deletes the lines of the example endpoint, with the comment directly above it. When it is the only
// endpoint, the whole `endpoints:` key goes. Comments directly above the next key or endpoint are kept.
func removeExample(data []byte, loc exampleLocation) ([]byte, error) {
	seq := loc.root.Content[loc.keyIndex+1]
	if seq.Style&yaml.FlowStyle != 0 || loc.items()[loc.item].Style&yaml.FlowStyle != 0 {
		return nil, errors.New("the endpoints are written in flow style ([...] or {...})")
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	// start and next are 1-based lines: the first line of the block and the line of the node after it.
	var start, next int
	items := loc.items()
	if len(items) == 1 {
		start = loc.root.Content[loc.keyIndex].Line
		next = len(lines) + 1
		if loc.keyIndex+2 < len(loc.root.Content) {
			next = loc.root.Content[loc.keyIndex+2].Line
		}
	} else {
		start = dashLine(lines, items[loc.item].Line)
		switch {
		case loc.item+1 < len(items):
			next = dashLine(lines, items[loc.item+1].Line)
		case loc.keyIndex+2 < len(loc.root.Content):
			next = loc.root.Content[loc.keyIndex+2].Line
		default:
			next = len(lines) + 1
		}
	}
	from, to := start-1, next-1
	for from > 0 && isCommentLine(lines[from-1]) {
		from--
	}
	for to > start && isCommentLine(lines[to-1]) {
		to--
	}
	for to > start && isBlankLine(lines[to-1]) {
		to--
	}
	if from > 0 && isBlankLine(lines[from-1]) {
		for to < next-1 && isBlankLine(lines[to]) {
			to++
		}
	}
	out := []byte(strings.Join(append(append([]string(nil), lines[:from]...), lines[to:]...), ""))

	before := len(loc.root.Content)
	root := parseRoot(out)
	if root == nil && strings.TrimSpace(string(out)) != "" {
		return nil, errors.New("the file would no longer be valid YAML")
	}
	if _, still := findExample(out); still {
		return nil, errors.New("the example endpoint could not be found on its own lines")
	}
	after := 0
	if root != nil {
		after = len(root.Content)
	}
	wantKeys := before
	if len(items) == 1 {
		wantKeys -= 2
	} else if k := endpointsIndex(root); k < 0 || len(root.Content[k+1].Content) != len(items)-1 {
		return nil, errors.New("removing the example endpoint would change other endpoints")
	}
	if after != wantKeys {
		return nil, errors.New("removing the example endpoint would change other settings")
	}
	return out, nil
}

// dashLine returns the line of the `-` of a sequence item: the item itself, or the line above when the dash stands alone.
func dashLine(lines []string, line int) int {
	if line >= 2 && strings.TrimSpace(lines[line-2]) == "-" {
		return line - 1
	}
	return line
}

func isCommentLine(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "#") }
func isBlankLine(s string) bool   { return strings.TrimSpace(s) == "" }
