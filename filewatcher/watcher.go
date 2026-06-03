// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package filewatcher

import (
	"context"
	"path/filepath"
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

	reloadCh := make(<-chan struct{})
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error(err, "unable to create file watcher; hot-reload disabled")
	} else {
		defer watcher.Close()
		// Watch the parent directory to handle Kubernetes atomic Secret volume
		// updates which swap a "..data" symlink rather than writing the file directly.
		if err := watcher.Add(filepath.Dir(path)); err != nil {
			log.Error(err, "unable to watch file directory; hot-reload disabled")
		} else {
			reloadCh = debounceWatcher(watcher, filepath.Base(path))
		}
	}

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

// debounceWatcher reads from watcher and returns a channel that receives a
// signal after a 200ms quiet period whenever the watched file (or the
// Kubernetes atomic-writer "..data" symlink) changes.
func debounceWatcher(watcher *fsnotify.Watcher, filename string) <-chan struct{} {
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
				if (base == filename || base == "..data") &&
					(event.Has(fsnotify.Create) || event.Has(fsnotify.Write) || event.Has(fsnotify.Rename)) {
					// Each matching event resets the timer. Only when 200ms pass
					// with no further events does the reload signal fire.
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
