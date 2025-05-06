// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

func TestSource(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Source Suite")
}

type MockSource struct {
	images []cloudprofilesync.SourceImage
}

func (m *MockSource) GetVersions(ctx context.Context) ([]cloudprofilesync.SourceImage, error) {
	return m.images, nil
}

const registryAddr = "127.0.0.1:8080"

var (
	runnable   cloudprofilesync.Runnable
	mockSource MockSource
	reg        *registry.Registry
	stop       context.CancelFunc
)

var _ = BeforeSuite(func() {
	mockSource = MockSource{}
	runnable = cloudprofilesync.Runnable{
		Log: GinkgoLogr,
		Source: cloudprofilesync.NamedSource{
			Name:   "test",
			Source: &mockSource,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop = cancel
	var err error

	reg, err = registry.NewRegistry(ctx, &configuration.Configuration{
		Storage:    configuration.Storage{"inmemory": map[string]any{}},
		HTTP:       configuration.HTTP{Addr: registryAddr},
		Validation: configuration.Validation{Disabled: true},
	})
	Expect(err).To(Succeed())
	go func() {
		defer GinkgoRecover()
		Expect(reg.ListenAndServe()).To(MatchError(http.ErrServerClosed))
	}()
})

var _ = AfterSuite(func(ctx SpecContext) {
	stop()
	Expect(reg.Shutdown(ctx)).To(Succeed())
})
