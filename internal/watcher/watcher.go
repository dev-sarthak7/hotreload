// Package watcher watches a directory tree for relevant file changes,
// handles dynamic folder creation/deletion, and respects OS watch limits.
package watcher

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// ignoredDirs are directories we never watch.
var ignoredDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".idea":        true,
	".vscode":      true,
	"dist":         true,
	"build":        true,
	"bin":          true,
	".cache":       true,
	"__pycache__":  true,
}

// ignoredExts are file extensions we don't care about.
var ignoredExts = map[string]bool{
	".swp":  true, // vim swap
	".swx":  true,
	".swo":  true,
	"~":     true, // emacs backup
	".tmp":  true,
	".bak":  true,
	".orig": true,
	".log":  true,
}

// ignoredPrefixes — temp files editors create
var ignoredPrefixes = []string{".", "#"}

// ChangedFile holds info about a changed file.
type ChangedFile struct {
	Path string
	Op   fsnotify.Op
}

// Watcher wraps fsnotify and adds recursive watching + filtering.
type Watcher struct {
	fw      *fsnotify.Watcher
	root    string
	logger  *slog.Logger
	changes chan ChangedFile
	mu      sync.Mutex
	watched map[string]bool
}

// New creates a new Watcher rooted at root.
func New(root string, logger *slog.Logger) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		fw.Close()
		return nil, err
	}

	w := &Watcher{
		fw:      fw,
		root:    abs,
		logger:  logger,
		changes: make(chan ChangedFile, 64),
		watched: make(map[string]bool),
	}

	// Recursively add all existing directories
	if err := w.addTree(abs); err != nil {
		fw.Close()
		return nil, err
	}

	go w.loop()
	return w, nil
}

// Changes returns the channel of file change events.
func (w *Watcher) Changes() <-chan ChangedFile {
	return w.changes
}

// Close stops the watcher.
func (w *Watcher) Close() {
	w.fw.Close()
}

// addTree recursively adds all non-ignored directories under root to the watcher.
func (w *Watcher) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Permission errors etc — log and skip
			w.logger.Debug("walk error", "path", path, "error", err)
			return nil
		}
		if d.IsDir() {
			if shouldIgnoreDir(d.Name()) {
				w.logger.Debug("skipping ignored dir", "path", path)
				return filepath.SkipDir
			}
			return w.addDir(path)
		}
		return nil
	})
}

// addDir adds a single directory to the fsnotify watcher.
func (w *Watcher) addDir(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.watched[path] {
		return nil
	}
	if err := w.fw.Add(path); err != nil {
		w.logger.Warn("could not watch directory", "path", path, "error", err)
		return nil // non-fatal
	}
	w.watched[path] = true
	w.logger.Debug("watching directory", "path", path)
	return nil
}

// removeDir removes a directory from tracking.
func (w *Watcher) removeDir(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.fw.Remove(path)
	delete(w.watched, path)
	w.logger.Debug("stopped watching directory", "path", path)
}

// loop is the background goroutine that processes fsnotify events.
func (w *Watcher) loop() {
	defer close(w.changes)

	for {
		select {
		case event, ok := <-w.fw.Events:
			if !ok {
				return
			}
			w.handleEvent(event)

		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			w.logger.Warn("watcher error", "error", err)
		}
	}
}

func (w *Watcher) handleEvent(event fsnotify.Event) {
	path := event.Name
	base := filepath.Base(path)

	// Handle new directories — add them to the watch list dynamically
	if event.Has(fsnotify.Create) {
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			if !shouldIgnoreDir(base) {
				w.logger.Info("new directory detected, watching", "path", path)
				_ = w.addTree(path)
			}
			return // directory creation itself isn't a code change
		}
	}

	// Handle deleted directories — remove from tracking
	if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
		w.mu.Lock()
		if w.watched[path] {
			w.mu.Unlock()
			w.removeDir(path)
			return
		}
		w.mu.Unlock()
	}

	// Filter out files we don't care about
	if !shouldWatch(path) {
		return
	}

	w.logger.Debug("file changed", "path", path, "op", event.Op)

	// Non-blocking send — if the channel is full, drop (debouncer handles batching)
	select {
	case w.changes <- ChangedFile{Path: path, Op: event.Op}:
	default:
		w.logger.Debug("change channel full, dropping event", "path", path)
	}
}

// shouldIgnoreDir returns true if this directory name should be skipped entirely.
func shouldIgnoreDir(name string) bool {
	if ignoredDirs[name] {
		return true
	}
	// Hidden directories (starting with .)
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

// shouldWatch returns true if this file path is relevant for triggering rebuilds.
func shouldWatch(path string) bool {
	base := filepath.Base(path)

	// Ignore by prefix
	for _, prefix := range ignoredPrefixes {
		if strings.HasPrefix(base, prefix) {
			return false
		}
	}

	// Ignore by suffix/extension
	if strings.HasSuffix(base, "~") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(base))
	if ignoredExts[ext] {
		return false
	}

	// Only watch Go files (and optionally config files)
	return ext == ".go" || ext == ".mod" || ext == ".sum" || ext == ".env" || ext == ".yaml" || ext == ".yml" || ext == ".json" || ext == ".toml"
}
