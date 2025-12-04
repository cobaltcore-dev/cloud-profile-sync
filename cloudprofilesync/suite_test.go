// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

func TestSource(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cloudprofilesync Suite")
}

type MockSource struct {
	images []cloudprofilesync.SourceImage
}

func (m *MockSource) GetVersions(ctx context.Context) ([]cloudprofilesync.SourceImage, error) {
	return m.images, nil
}

type MockProvider struct{}

func (m *MockProvider) Configure(cpSpec *gardenerv1beta1.CloudProfileSpec, versions []cloudprofilesync.SourceImage) error {
	data, err := json.Marshal(versions)
	if err != nil {
		return err
	}
	cpSpec.ProviderConfig = &runtime.RawExtension{Raw: data}
	return nil
}

const registryAddr = "127.0.0.1:8080"

var (
	mockSource MockSource
	reg        *registry.Registry
	stop       context.CancelFunc
)

var _ = BeforeSuite(func() {
	mockSource = MockSource{}
	ctx, cancel := context.WithCancel(context.Background())
	stop = cancel
	var err error

	reg, err = registry.NewRegistry(ctx, &configuration.Configuration{
		Storage:    configuration.Storage{"inmemory": map[string]any{}},
		HTTP:       configuration.HTTP{Addr: registryAddr},
		Validation: configuration.Validation{Disabled: true},
		Log:        configuration.Log{Level: "error", AccessLog: configuration.AccessLog{Disabled: true}},
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
