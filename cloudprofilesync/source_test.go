// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"bytes"
	"encoding/json"
	"strings"

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

})
