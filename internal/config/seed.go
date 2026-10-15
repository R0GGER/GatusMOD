package config

import (
	"io/fs"
	"os"
	"path/filepath"
)

// SeedExample copies the example files into path when it is a directory without any configuration file,
// so a fresh install with an empty bind mount starts. Existing files are never overwritten.
// It reports whether the example was written.
func SeedExample(path string, example fs.FS) (bool, error) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	if files, err := configFiles(path); err != nil || len(files) > 0 {
		return false, err
	}
	err = fs.WalkDir(example, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(path, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Stat(target); err == nil {
			return nil
		}
		data, err := fs.ReadFile(example, p)
		if err != nil {
			return err
		}
		// Gatus watches the directory, so it must never see a half-written .yaml file.
		tmp := target + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, target)
	})
	return err == nil, err
}
