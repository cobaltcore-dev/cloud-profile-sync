// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package filewatcher_test

import (
	"context"
	"os"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/filewatcher"
)

var _ = Describe("RunWithFileUpdate", func() {
	It("restarts when the watched file is updated", func() {
		f, err := os.CreateTemp("", "watched-*.yaml")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(f.Name())
		_, err = f.WriteString("version: 1")
		Expect(err).NotTo(HaveOccurred())
		f.Close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var startCount atomic.Int32

		// runFunc blocks until its context is cancelled, simulating a running manager.
		runFunc := func(runCtx context.Context) {
			startCount.Add(1)
			<-runCtx.Done()
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			filewatcher.RerunOnFileUpdate(ctx, f.Name(), runFunc)
		}()

		// Wait for the first start.
		Eventually(startCount.Load, 2*time.Second, 10*time.Millisecond).Should(BeNumerically("==", 1))

		// Update the file to trigger a reload.
		Expect(os.WriteFile(f.Name(), []byte("version: 2"), 0600)).To(Succeed())

		// The debounce period is 200ms; give it a comfortable margin.
		Eventually(startCount.Load, 2*time.Second, 10*time.Millisecond).Should(BeNumerically("==", 2))

		// Cancel the outer context and confirm the loop exits.
		cancel()
		Eventually(done, 2*time.Second).Should(BeClosed())
	})
})
