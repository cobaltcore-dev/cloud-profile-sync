// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/scheme"
	"k8s.io/client-go/rest"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/controllers"
)

const registryAddr = "127.0.0.1:48081"

var (
	cfg        *rest.Config
	k8sClient  client.Client
	k8sManager ctrl.Manager
	testEnv    *envtest.Environment
	reg        *registry.Registry
	stop       context.CancelFunc
	reconciler *controllers.Reconciler
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controllers Suite")
}

var _ = BeforeSuite(func(ctx SpecContext) {
	SetDefaultEventuallyTimeout(3 * time.Second)
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "crd")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).To(Succeed())
	Expect(cfg).ToNot(BeNil())

	Expect(corev1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(gardenerv1beta1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(v1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).To(Succeed())

	reconciler = &controllers.Reconciler{
		Client: k8sManager.GetClient(),
	}
	err = reconciler.SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	stopCtx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	stop = cancel
	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(stopCtx)
		Expect(err).ToNot(HaveOccurred())
	}()

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).To(Succeed())
	Expect(k8sClient).ToNot(BeNil())

	reg, err = registry.NewRegistry(stopCtx, &configuration.Configuration{
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
	Eventually(func(g Gomega) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+registryAddr, http.NoBody)
		g.Expect(err).To(Succeed())
		res, err := http.DefaultClient.Do(req)
		g.Expect(err).To(Succeed())
		defer res.Body.Close()
		return nil
	}).Should(Succeed())

	repo, err := remote.NewRepository(registryAddr + "/repo")
	Expect(err).To(Succeed())
	repo.PlainHTTP = true

	index := ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		Manifests: []ocispec.Descriptor{
			{
				MediaType: ocispec.MediaTypeImageManifest,
				Size:      0,
				Digest:    ocispec.DescriptorEmptyJSON.Digest,
			},
		},
		Annotations: map[string]string{
			"architecture": "amd64",
			"created":      time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
		},
	}
	indexBlob, err := json.Marshal(index)
	Expect(err).To(Succeed())

	Expect(err).To(Succeed())
	indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)

	err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
	Expect(err).To(Succeed())
	err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "1.0.0")
	Expect(err).To(Succeed())

	err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
	Expect(err).To(Succeed())
	err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "1.0.1_abc")
	Expect(err).To(Succeed())
})

var _ = AfterSuite(func(ctx SpecContext) {
	stop()
	Expect(reg.Shutdown(ctx)).To(Succeed())
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})
