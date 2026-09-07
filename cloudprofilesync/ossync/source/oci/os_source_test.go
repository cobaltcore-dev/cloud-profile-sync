// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package oci_test

import (
	"bytes"
	"encoding/json"
	"strings"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ocirepo"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/source/oci"
)

var _ = Describe("OCISource", func() {

	It("retrieves versions from a registry", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			SchemaVersion: 2,
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

		oci, err := oci.NewOCI(ocirepo.Params{
			Registry:   registryAddr,
			Repository: "repo",
			Insecure:   true,
		}, 4, logr.Discard(), nil, nil)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(2))
		Expect(versions).To(ContainElement(
			ossync.SourceImage{
				Version:       "1.0.0",
				Architectures: []string{"amd64"},
			}))
		Expect(versions).To(ContainElement(
			ossync.SourceImage{
				Version:       "1.0.1+abc",
				Architectures: []string{"amd64"},
			}))
	})

	It("expands featureSetCapabilities into boolean capabilities from feature_set annotation", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-bool-caps")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			SchemaVersion: 2,
			Manifests: []ocispec.Descriptor{
				{
					MediaType: ocispec.MediaTypeImageManifest,
					Size:      0,
					Digest:    ocispec.DescriptorEmptyJSON.Digest,
				},
			},
			Annotations: map[string]string{
				"architecture": "amd64",
				"feature_set":  "scibase,_usi",
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

		// vhost is absent → false; usidev is absent → false
		oci, err := oci.NewOCI(ocirepo.Params{
			Registry:   registryAddr,
			Repository: "repo-bool-caps",
			Insecure:   true,
		}, 4, logr.Discard(), map[string]string{
			"vhost":   "vhost",
			"_usidev": "usidev",
		}, nil)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].Version).To(Equal("2.0.0"))
		Expect(versions[0].CleanVersion).To(Equal("2.0.0"))
		Expect(versions[0].Capabilities).To(Equal(gardencorev1beta1.Capabilities{
			"architecture": {"amd64"},
			"vhost":        {"false"},
			"usidev":       {"false"},
		}))
	})

	It("sets capability to true when featureSetValue is present in feature_set annotation", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-bool-caps-true")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			SchemaVersion: 2,
			Manifests: []ocispec.Descriptor{
				{
					MediaType: ocispec.MediaTypeImageManifest,
					Size:      0,
					Digest:    ocispec.DescriptorEmptyJSON.Digest,
				},
			},
			Annotations: map[string]string{
				"architecture": "amd64",
				"feature_set":  "sci,_usi,vhost",
				"version":      "3.0.0",
			},
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)

		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "3.0.0")
		Expect(err).To(Succeed())

		// vhost is present → true; usidev is absent → false
		oci, err := oci.NewOCI(ocirepo.Params{
			Registry:   registryAddr,
			Repository: "repo-bool-caps-true",
			Insecure:   true,
		}, 4, logr.Discard(), map[string]string{
			"vhost":   "vhost",
			"_usidev": "usidev",
		}, nil)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].Capabilities).To(Equal(gardencorev1beta1.Capabilities{
			"architecture": {"amd64"},
			"vhost":        {"true"},
			"usidev":       {"false"},
		}))
	})

	It("matches FeatureSetValue exactly against raw annotation tokens", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-underscore-caps")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			SchemaVersion: 2,
			Manifests: []ocispec.Descriptor{
				{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest},
			},
			Annotations: map[string]string{
				"architecture": "amd64",
				"feature_set":  "sci,_usi,_usidev",
				"version":      "6.0.0",
			},
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)

		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "6.0.0")
		Expect(err).To(Succeed())

		// FeatureSetValue must match the raw annotation token exactly.
		// "_usidev" matches the annotation; "vhost" does not appear → false.
		oci, err := oci.NewOCI(ocirepo.Params{
			Registry:   registryAddr,
			Repository: "repo-underscore-caps",
			Insecure:   true,
		}, 4, logr.Discard(), map[string]string{
			"vhost":   "vhost",
			"_usidev": "usidev",
		}, nil)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].Capabilities).To(Equal(gardencorev1beta1.Capabilities{
			"architecture": {"amd64"},
			"vhost":        {"false"},
			"usidev":       {"true"},
		}))
	})

	It("leaves Capabilities nil when only architecture annotation is present", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-legacy")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			SchemaVersion: 2,
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

		oci, err := oci.NewOCI(ocirepo.Params{
			Registry:   registryAddr,
			Repository: "repo-legacy",
			Insecure:   true,
		}, 4, logr.Discard(), nil, nil)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].Capabilities).To(BeNil())
	})

	It("skips tags without architecture annotation and returns remaining images", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-missing-arch")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		withArch := ocispec.Index{
			SchemaVersion: 2,
			Manifests: []ocispec.Descriptor{
				{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest},
			},
			Annotations: map[string]string{
				"architecture": "amd64",
			},
		}
		withArchBlob, err := json.Marshal(withArch)
		Expect(err).To(Succeed())
		withArchDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, withArchBlob)

		noArch := ocispec.Index{
			SchemaVersion: 2,
			Manifests: []ocispec.Descriptor{
				{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest},
			},
		}
		noArchBlob, err := json.Marshal(noArch)
		Expect(err).To(Succeed())
		noArchDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, noArchBlob)

		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, withArchDesc, bytes.NewReader(withArchBlob), "1.0.0")
		Expect(err).To(Succeed())
		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, noArchDesc, bytes.NewReader(noArchBlob), "1.0.1")
		Expect(err).To(Succeed())

		oci, err := oci.NewOCI(ocirepo.Params{
			Registry:   registryAddr,
			Repository: "repo-missing-arch",
			Insecure:   true,
		}, 4, logr.Discard(), nil, nil)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].Version).To(Equal("1.0.0"))
	})

	It("detects SupportInPlaceUpdate from feature_set even when featureSetCapabilities is empty", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-usi-no-caps")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			SchemaVersion: 2,
			Manifests: []ocispec.Descriptor{
				{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest},
			},
			Annotations: map[string]string{
				"architecture": "amd64",
				"feature_set":  "sci,_usi",
				"version":      "4.0.0",
			},
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)

		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "4.0.0")
		Expect(err).To(Succeed())

		oci, err := oci.NewOCI(ocirepo.Params{
			Registry:   registryAddr,
			Repository: "repo-usi-no-caps",
			Insecure:   true,
		}, 4, logr.Discard(), nil, nil)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].SupportInPlaceUpdate).To(BeTrue())
		Expect(versions[0].Capabilities).To(BeNil())
	})

	It("populates CleanVersion from version annotation even when featureSetCapabilities is empty", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-clean-version-no-caps")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			SchemaVersion: 2,
			Manifests: []ocispec.Descriptor{
				{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest},
			},
			Annotations: map[string]string{
				"architecture": "amd64",
				"version":      "5.0.0",
				"feature_set":  "sci,_usi",
			},
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)

		err = repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))
		Expect(err).To(Succeed())
		err = repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "5.0.0-build-abc")
		Expect(err).To(Succeed())

		oci, err := oci.NewOCI(ocirepo.Params{
			Registry:   registryAddr,
			Repository: "repo-clean-version-no-caps",
			Insecure:   true,
		}, 4, logr.Discard(), nil, nil)
		Expect(err).To(Succeed())
		versions, err := oci.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].CleanVersion).To(Equal("5.0.0"))
		Expect(versions[0].Capabilities).To(BeNil())
		Expect(versions[0].SupportInPlaceUpdate).To(BeTrue())
	})

	It("returns false for SupportInPlaceUpdate when feature_set lacks _usi", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-no-usi")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		index := ocispec.Index{
			SchemaVersion: 2,
			Manifests:     []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest}},
			Annotations: map[string]string{
				"architecture": "amd64",
				"feature_set":  "scibase,vhost",
				"version":      "7.0.0",
			},
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)
		Expect(repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))).To(Succeed())
		Expect(repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), "7.0.0")).To(Succeed())

		o, err := oci.NewOCI(ocirepo.Params{Registry: registryAddr, Repository: "repo-no-usi", Insecure: true}, 4, logr.Discard(), nil, nil)
		Expect(err).To(Succeed())
		versions, err := o.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(1))
		Expect(versions[0].SupportInPlaceUpdate).To(BeFalse())
	})

	It("returns an error when all tags are skipped due to missing annotations", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/repo-all-bad")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true

		noArch := ocispec.Index{
			SchemaVersion: 2,
			Manifests:     []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest}},
			Annotations:   map[string]string{"feature_set": "scibase"},
		}
		noArchBlob, err := json.Marshal(noArch)
		Expect(err).To(Succeed())
		noArchDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, noArchBlob)
		Expect(repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))).To(Succeed())
		Expect(repo.PushReference(ctx, noArchDesc, bytes.NewReader(noArchBlob), "1.0.0")).To(Succeed())
		Expect(repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))).To(Succeed())
		Expect(repo.PushReference(ctx, noArchDesc, bytes.NewReader(noArchBlob), "2.0.0")).To(Succeed())

		o, err := oci.NewOCI(ocirepo.Params{Registry: registryAddr, Repository: "repo-all-bad", Insecure: true}, 4, logr.Discard(), nil, nil)
		Expect(err).To(Succeed())
		_, err = o.GetVersions(ctx)
		Expect(err).To(MatchError(ContainSubstring("all 2 tags were skipped")))
	})

})

var _ = Describe("OCI imageFilter", func() {
	pushImage := func(ctx SpecContext, repo *remote.Repository, tag string, annotations map[string]string) {
		index := ocispec.Index{
			SchemaVersion: 2,
			Manifests:     []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageManifest, Size: 0, Digest: ocispec.DescriptorEmptyJSON.Digest}},
			Annotations:   annotations,
		}
		indexBlob, err := json.Marshal(index)
		Expect(err).To(Succeed())
		indexDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, indexBlob)
		Expect(repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}"))).To(Succeed())
		Expect(repo.PushReference(ctx, indexDesc, bytes.NewReader(indexBlob), tag)).To(Succeed())
	}

	It("includes all images when imageFilter is nil", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/filter-nil")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true
		pushImage(ctx, repo, "1.0.0", map[string]string{"architecture": "amd64", "feature_set": "scibase,_pxe", "version": "1.0.0"})
		pushImage(ctx, repo, "2.0.0", map[string]string{"architecture": "amd64", "feature_set": "scibase,_usi", "version": "2.0.0"})

		o, err := oci.NewOCI(ocirepo.Params{Registry: registryAddr, Repository: "filter-nil", Insecure: true}, 4, logr.Discard(), nil, nil)
		Expect(err).To(Succeed())
		versions, err := o.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(2))
	})

	It("excludes images missing a required feature_set value (exact raw match)", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/filter-required")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true
		pushImage(ctx, repo, "1.0.0", map[string]string{"architecture": "amd64", "feature_set": "scibase,_pxe", "version": "1.0.0"})
		pushImage(ctx, repo, "2.0.0", map[string]string{"architecture": "amd64", "feature_set": "scibase,_usi", "version": "2.0.0"})
		pushImage(ctx, repo, "3.0.0", map[string]string{"architecture": "amd64", "feature_set": "scibase,_usi,vhost", "version": "3.0.0"})

		filter := &v1alpha1.ImageFilter{RequiredFeatureSetValues: []string{"scibase", "_usi"}}
		o, err := oci.NewOCI(ocirepo.Params{Registry: registryAddr, Repository: "filter-required", Insecure: true}, 4, logr.Discard(), nil, filter)
		Expect(err).To(Succeed())
		versions, err := o.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(HaveLen(2))
		versionStrings := []string{versions[0].CleanVersion, versions[1].CleanVersion}
		Expect(versionStrings).To(ConsistOf("2.0.0", "3.0.0"))
	})

	It("returns empty when no images pass the filter", func(ctx SpecContext) {
		repo, err := remote.NewRepository(registryAddr + "/filter-none")
		Expect(err).To(Succeed())
		repo.PlainHTTP = true
		pushImage(ctx, repo, "1.0.0", map[string]string{"architecture": "amd64", "feature_set": "scibase,_pxe", "version": "1.0.0"})
		pushImage(ctx, repo, "2.0.0", map[string]string{"architecture": "amd64", "feature_set": "scibase,capi", "version": "2.0.0"})

		filter := &v1alpha1.ImageFilter{RequiredFeatureSetValues: []string{"_usi"}}
		o, err := oci.NewOCI(ocirepo.Params{Registry: registryAddr, Repository: "filter-none", Insecure: true}, 4, logr.Discard(), nil, filter)
		Expect(err).To(Succeed())
		versions, err := o.GetVersions(ctx)
		Expect(err).To(Succeed())
		Expect(versions).To(BeEmpty())
	})
})
