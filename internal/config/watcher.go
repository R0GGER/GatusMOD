package config

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Watcher holds the current configuration and reloads it when one of the configuration files changes.
// An invalid new configuration is logged and the previous one stays active.
type Watcher struct {
	path     string
	current  atomic.Pointer[Config]
	modTime  time.Time
	onChange func(*Config)
}

func NewWatcher(path string) (*Watcher, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	w := &Watcher{path: path}
	w.current.Store(cfg)
	w.modTime, _ = LatestModTime(path)
	return w, nil
}

func (w *Watcher) Get() *Config { return w.current.Load() }

// OnChange registers a callback invoked after a successful reload. Must be called before Run.
func (w *Watcher) OnChange(fn func(*Config)) { w.onChange = fn }

func (w *Watcher) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		modTime, err := LatestModTime(w.path)
		if err != nil || !modTime.After(w.modTime) {
			continue
		}
		w.modTime = modTime
		cfg, err := Load(w.path)
		if err != nil {
			slog.Error("configuration changed but is invalid, keeping the previous configuration", "error", err)
			continue
		}
		w.current.Store(cfg)
		slog.Info("configuration reloaded")
		if w.onChange != nil {
			w.onChange(cfg)
		}
	}
}
