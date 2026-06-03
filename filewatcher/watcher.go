// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package filewatcher

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/fsnotify/fsnotify"
	ctrl "sigs.k8s.io/controller-runtime"
)

const fileEventsDebouncePeriod = 200 * time.Millisecond

var log = ctrl.Log.WithName("filewatcher")

// RerunOnFileUpdate watches the file at path and calls runFunc on each start
// and whenever the file changes. runFunc receives a child context that is
// cancelled when a file change is detected; the outer ctx cancels the loop.
func RerunOnFileUpdate(ctx context.Context, path string, runFunc func(ctx context.Context)) {
	log.Info("watching file", "path", path)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error(err, "unable to create file watcher; hot-reload disabled")
		runFunc(ctx)

		return
	}

	defer watcher.Close()
	// Watch the parent directory to handle Kubernetes atomic Secret volume
	// updates which swap a "..data" symlink rather than writing the file directly.
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		log.Error(err, "unable to watch file directory; hot-reload disabled")
		runFunc(ctx)

		return
	}

	reloadCh := watchFileEvents(watcher, filepath.Base(path))

	for {
		runCtx, runCancel := context.WithCancel(ctx)

		go func() {
			select {
			case <-runCtx.Done():
			case <-reloadCh:
				log.Info("file changed, restarting")
				runCancel()
			}
		}()

		runFunc(runCtx)
		runCancel()
		if ctx.Err() != nil {
			return
		}

		log.Info("restarting after file change", "path", path)
	}
}

// watchFileEvents returns a channel that receives a signal after a 200ms quiet
// period whenever the watched file (or the Kubernetes atomic-writer "..data"
// symlink) changes. The debounce prevents multiple rapid filesystem events
// (e.g. Write + Chmod from a single logical write) from triggering redundant
// reloads. The channel has a buffer of 1: if a reload is already pending,
// further signals are dropped — safe because the reload always reads the latest
// state of the file.
func watchFileEvents(watcher *fsnotify.Watcher, filename string) <-chan struct{} {
	reloadCh := make(chan struct{}, 1)
	go func() {
		var debounce <-chan time.Time
		for {
			select {
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Error(err, "file watcher error")
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				base := filepath.Base(event.Name)
				notificationEvents := []fsnotify.Op{fsnotify.Create, fsnotify.Write, fsnotify.Rename}

				if (base == filename || base == "..data") &&
					slices.Contains(notificationEvents, event.Op) {
					debounce = time.After(fileEventsDebouncePeriod)
				}
			case <-debounce:
				debounce = nil
				select {
				case reloadCh <- struct{}{}:
				default: // reload already pending, drop
				}
			}
		}
	}()

	return reloadCh
}
