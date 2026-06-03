// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package filewatcher_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/filewatcher"
)

var _ = Describe("RunWithFileUpdate (symlink)", func() {
	It("restarts when the watched file is updated via symlink swap (Kubernetes atomic write)", func() {
		// Simulate the Kubernetes atomic Secret/ConfigMap volume update pattern:
		// a "..data" symlink in the watched directory is replaced atomically,
		// which fsnotify (via inotify on Linux) reports as a Rename event on "..data".
		// This pattern does not emit directory-level events on macOS (kqueue).
		dir, err := os.MkdirTemp("", "watched-symlink-*")
		Expect(err).NotTo(HaveOccurred())
		defer os.RemoveAll(dir)

		// Create two versioned data directories and a token file in each.
		dataDir1 := filepath.Join(dir, "..2024_01_01")
		dataDir2 := filepath.Join(dir, "..2024_01_02")
		Expect(os.Mkdir(dataDir1, 0700)).To(Succeed())
		Expect(os.Mkdir(dataDir2, 0700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dataDir1, "token"), []byte("token-v1"), 0600)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dataDir2, "token"), []byte("token-v2"), 0600)).To(Succeed())

		// Point "..data" symlink at the first data directory, then expose
		// "token" as a symlink into it — matching the Kubernetes projection layout.
		dataSymlink := filepath.Join(dir, "..data")
		Expect(os.Symlink(dataDir1, dataSymlink)).To(Succeed())
		Expect(os.Symlink(filepath.Join(dataSymlink, "token"), filepath.Join(dir, "token"))).To(Succeed())

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var startCount atomic.Int32
		runFunc := func(runCtx context.Context) {
			startCount.Add(1)
			<-runCtx.Done()
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			filewatcher.RerunOnFileUpdate(ctx, filepath.Join(dir, "token"), runFunc)
		}()

		Eventually(startCount.Load, 2*time.Second, 10*time.Millisecond).Should(BeNumerically("==", 1))

		// Swap "..data" to the new directory atomically (rename a tmp symlink over it).
		tmpSymlink := filepath.Join(dir, "..data_tmp")
		Expect(os.Symlink(dataDir2, tmpSymlink)).To(Succeed())
		Expect(os.Rename(tmpSymlink, dataSymlink)).To(Succeed())

		Eventually(startCount.Load, 2*time.Second, 10*time.Millisecond).Should(BeNumerically("==", 2))

		cancel()
		Eventually(done, 2*time.Second).Should(BeClosed())
	})
})
