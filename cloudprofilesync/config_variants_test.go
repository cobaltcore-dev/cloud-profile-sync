// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

func buildConfigBytes(variantRegex string) []byte {
	cfg := map[string]any{
		"cloudProfile": "test-profile",
		"source": map[string]any{
			"name": "test-src",
			"oci": map[string]any{
				"registry":   "example.io",
				"repository": "repo",
				"parallel":   1,
			},
			"variants": []map[string]any{{
				"regex":             variantRegex,
				"imageNameTemplate": "img-{{variant}}",
			}},
		},
		"provider": map[string]any{
			"ironcore": map[string]any{
				"registry":   "example.io",
				"repository": "prov-repo",
				"imageName":  "prov-img",
			},
		},
	}
	b, _ := json.Marshal(cfg)
	return b
}

var _ = Describe("LoadConfig variant regex validation", func() {
	It("fails for malformed regex syntax", func() {
		malformedPattern := "^(?P<version>\\d+\\.\\d+\\.\\d+-(?P<variant>.+)$" // missing closing parenthesis
		_, err := cloudprofilesync.LoadConfig(buildConfigBytes(malformedPattern))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid variant regex"))
	})

	It("fails when 'variant' group missing", func() {
		missingVariantPattern := "^(?P<version>\\d+\\.\\d+\\.\\d+)-[a-z]+$" // no 'variant' group
		_, err := cloudprofilesync.LoadConfig(buildConfigBytes(missingVariantPattern))
		Expect(err).To(HaveOccurred())
		Expect(
			err.Error(),
		).To(ContainSubstring("must contain named groups 'version' and 'variant'"))
	})

	It("fails when 'version' group missing", func() {
		missingVersionPattern := "^metal-(?P<variant>.+)$" // no 'version' group
		_, err := cloudprofilesync.LoadConfig(buildConfigBytes(missingVersionPattern))
		Expect(err).To(HaveOccurred())
		Expect(
			err.Error(),
		).To(ContainSubstring("must contain named groups 'version' and 'variant'"))
	})

	It("succeeds when both required groups present", func() {
		validPattern := "^(?P<version>\\d+\\.\\d+\\.\\d+)-metal-(?P<variant>[a-z0-9-]+)$" // both groups present
		cfg, err := cloudprofilesync.LoadConfig(buildConfigBytes(validPattern))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Source.Extractors).To(HaveLen(1))
		Expect(cfg.Source.Extractors[0].Rule.ImageNameTemplate).To(Equal("img-{{variant}}"))
	})
})
