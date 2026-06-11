// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"bytes"
	"encoding/json"
	"strings"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

var _ = Describe("OCISource", func() {

	It("retrieves versions from a registry", func(ctx SpecContext) {
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

		oci, err := cloudprofilesync.NewOCI(cloudprofilesync.OCIParams{
			Registry:   registryAddr,
			Repository: "repo",
			Parallel:   4,
		}, true)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(2))
		Expect(versions).To(ContainElement(
			cloudprofilesync.SourceImage{Version: "1.0.0", Architectures: []string{"amd64"}}))
		Expect(versions).To(ContainElement(
			cloudprofilesync.SourceImage{Version: "1.0.1+abc", Architectures: []string{"amd64"}}))
	})

	It("populates capabilities when feature_set annotation is present", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-caps")
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
				"feature_set":  "sci,_usi,_rescue,log",
				"version":      "2.0.0",
			},
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)

		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "2.0.0")
		Expect(err).To(Succeed())

		oci, err := cloudprofilesync.NewOCI(cloudprofilesync.OCIParams{
			Registry:   registryAddr,
			Repository: "repo-caps",
			Parallel:   4,
		}, true)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].Version).To(Equal("2.0.0"))
		Expect(versions[0].CleanVersion).To(Equal("2.0.0"))
		Expect(versions[0].Architectures).To(Equal([]string{"amd64"}))
		Expect(versions[0].Capabilities).To(Equal(gardencorev1beta1.Capabilities{
			"architecture": {"amd64"},
			"feature":      {"sci", "_usi"}, // _rescue and log are filtered out
		}))
	})

	It("leaves Capabilities nil when only architecture annotation is present", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-legacy")
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
			},
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)

		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "1.0.0-legacy")
		Expect(err).To(Succeed())

		oci, err := cloudprofilesync.NewOCI(cloudprofilesync.OCIParams{
			Registry:   registryAddr,
			Repository: "repo-legacy",
			Parallel:   4,
		}, true)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].Capabilities).To(BeNil())
	})

	It("leaves Capabilities nil when feature_set contains no valid values", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-no-valid-features")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			Versioned: specs.Versioned{SchemaVersion: 2},
			Manifests: []ocispec.Descriptor{
				{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest},
			},
			Annotations: map[string]string{
				"architecture": "amd64",
				"feature_set":  "_rescue,log,sap,ssh",
				"version":      "3.0.0",
			},
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)

		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "3.0.0-no-valid-features")
		Expect(err).To(Succeed())

		oci, err := cloudprofilesync.NewOCI(cloudprofilesync.OCIParams{
			Registry:   registryAddr,
			Repository: "repo-no-valid-features",
			Parallel:   4,
		}, true)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].Capabilities).To(BeNil())
		Expect(versions[0].CleanVersion).To(BeEmpty())
	})

})
