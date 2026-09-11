package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watchDebounce = 50 * time.Millisecond

func newAppWatcher(root string) (*fsnotify.Watcher, error) {
	w, err := fsnotify.NewBufferedWatcher(256)
	if err != nil {
		return nil, err
	}
	if err := syncWatchDirectories(w, root); err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

// syncWatchDirectories adds a non-recursive OS watch for every directory. Both
// inotify and kqueue require this to observe changes below the application root.
func syncWatchDirectories(w *fsnotify.Watcher, root string) error {
	root = filepath.Clean(root)
	parent := filepath.Dir(root)
	watched := make(map[string]bool)
	for _, path := range w.WatchList() {
		path = filepath.Clean(path)
		rel, err := filepath.Rel(root, path)
		inside := err == nil && rel != ".." && !filepath.IsAbs(rel)
		info, statErr := os.Stat(path)
		if (!inside && path != parent) || statErr != nil || !info.IsDir() {
			_ = w.Remove(path)
			continue
		}
		watched[path] = true
	}
	if !watched[parent] {
		if err := w.Add(parent); err != nil {
			return err
		}
		watched[parent] = true
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() || watched[path] {
			return nil
		}
		if err := w.Add(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func watchEventAffectsRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel)
}

func (g *Gateway) watch(ctx context.Context, w *fsnotify.Watcher) {
	defer func() { _ = w.Close() }()
	var timer *time.Timer
	var timerC <-chan time.Time
	queue := func() {
		if timer == nil {
			timer = time.NewTimer(watchDebounce)
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(watchDebounce)
		}
		timerC = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			if watchEventAffectsRoot(g.cfg.AppsDir, event.Name) {
				queue()
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Printf("watch error: %v", err)
			queue()
		case <-timerC:
			timerC = nil
			if err := g.scan(); err != nil {
				log.Printf("scan error: %v", err)
			}
			if err := syncWatchDirectories(w, g.cfg.AppsDir); err != nil {
				log.Printf("watch reconciliation error: %v", err)
			}
		}
	}
}
