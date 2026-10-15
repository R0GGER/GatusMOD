package config

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestSeedExample(t *testing.T) {
	example := fstest.MapFS{
		"config.yaml":         {Data: []byte("gatusmod: {}\n")},
		"favicon/favicon.ico": {Data: []byte("ico")},
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "favicon"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "favicon/favicon.ico", "mine")
	seeded, err := SeedExample(dir, example)
	if err != nil || !seeded {
		t.Fatalf("empty directory: seeded=%v err=%v", seeded, err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "config.yaml")); string(data) != "gatusmod: {}\n" {
		t.Errorf("config.yaml = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "favicon", "favicon.ico")); string(data) != "mine" {
		t.Errorf("existing favicon was overwritten: %q", data)
	}
	if _, err := Load(dir); err != nil {
		t.Errorf("seeded directory does not load: %v", err)
	}

	other := t.TempDir()
	writeFile(t, other, "gatus.yml", "endpoints: []\n")
	if seeded, err := SeedExample(other, example); err != nil || seeded {
		t.Errorf("directory with a config file: seeded=%v err=%v", seeded, err)
	}
	if _, err := os.Stat(filepath.Join(other, "config.yaml")); err == nil {
		t.Error("config.yaml written next to an existing config file")
	}
}
