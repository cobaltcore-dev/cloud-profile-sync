// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

import (
	"context"
	"regexp"

	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type sourceFunc []SourceImage

func (s sourceFunc) GetVersions(ctx context.Context) ([]SourceImage, error) { return s, nil }

var _ = Describe("Runnable variant extraction", func() {
	var (
		reVariant *regexp.Regexp
		compiled  []CompiledExtractor
	)

	BeforeEach(func() {
		reVariant = regexp.MustCompile(
			`^(?P<version>\d+\.\d+\.\d+)-metal-(?P<variant>.+)-(?P<arch>[\d\w]+)-(?P<commit>[a-f0-9]{8})-(?P<arch2>amd64|arm64)$`,
		)
		compiled = []CompiledExtractor{{
			Rule: VariantExtractionRule{
				Regex:             reVariant.String(),
				ImageNameTemplate: "gardenlinux-{{variant}}",
			},
			Regex: reVariant,
		}}
	})

	Context("extract helper", func() {
		It("extracts expected variants", func() {
			cases := map[string]string{
				"2054.0.0-metal-scibase-usi-amd64-a3a50192-amd64": "gardenlinux-scibase-usi",
				"2053.0.0-metal-sci-usi-amd64-dc697cba-amd64":     "gardenlinux-sci-usi",
				"2053.0.0-metal-capi-amd64-dc697cba-amd64":        "gardenlinux-capi",
			}
			for tag, expectedName := range cases {
				v, name := extract(tag, compiled)
				Expect(v).NotTo(BeEmpty())
				Expect(name).To(Equal(expectedName))
			}
		})
	})

	Context("end-to-end runnable", func() {
		It("creates one image per valid tag", func() {
			r := &Runnable{Source: NamedSource{Extractors: compiled}}
			images := []SourceImage{
				{
					Version:       "2054.0.0-metal-scibase-usi-amd64-a3a50192-amd64",
					Architectures: []string{"amd64"},
				},
				{
					Version:       "2053.0.0-metal-sci-usi-amd64-dc697cba-amd64",
					Architectures: []string{"amd64"},
				},
				{
					Version:       "2053.0.0-metal-capi-amd64-dc697cba-amd64",
					Architectures: []string{"amd64"},
				},
			}
			r.Source.Source = sourceFunc(images)
			var cp v1beta1.CloudProfile
			Expect(r.CheckSource(nil, &cp)).To(Succeed())
			Expect(cp.Spec.MachineImages).To(HaveLen(3))
		})

		It("ignores invalid tags", func() {
			r := &Runnable{Source: NamedSource{Extractors: compiled}}
			invalid := []SourceImage{
				{
					Version:       "2054.0-metal-scibase-usi-amd64-a3a50192-amd64",
					Architectures: []string{"amd64"},
				}, // two-part version
				{
					Version:       "2054.0.0-vmware-capi-amd64-a3a50192-amd64",
					Architectures: []string{"amd64"},
				}, // wrong prefix
				{
					Version:       "2054.0.0-metal-capi-amd64-a3a5019-amd64",
					Architectures: []string{"amd64"},
				}, // short commit
				{
					Version:       "2054.0.0-metal-capi-amd64-a3a50192",
					Architectures: []string{"amd64"},
				}, // missing final arch
				{
					Version:       "2054.0.0-metal-scibase_usi-amd64-a3a50192-amd64",
					Architectures: []string{"amd64"},
				}, // underscore variant
			}
			r.Source.Source = sourceFunc(invalid)
			var cp v1beta1.CloudProfile
			Expect(r.CheckSource(nil, &cp)).To(Succeed())
			Expect(cp.Spec.MachineImages).To(BeEmpty())
		})

		It("mixes valid and invalid tags producing only valid images", func() {
			r := &Runnable{Source: NamedSource{Extractors: compiled}}
			mixed := []SourceImage{
				{
					Version:       "2054.0.0-metal-scibase-usi-amd64-a3a50192-amd64",
					Architectures: []string{"amd64"},
				}, // valid
				{
					Version:       "2054.0-metal-scibase-usi-amd64-a3a50192-amd64",
					Architectures: []string{"amd64"},
				}, // invalid
				{
					Version:       "2054.0.0-metal-capi-amd64-a3a5019-amd64",
					Architectures: []string{"amd64"},
				}, // invalid
			}
			r.Source.Source = sourceFunc(mixed)
			var cp v1beta1.CloudProfile
			Expect(r.CheckSource(nil, &cp)).To(Succeed())
			Expect(cp.Spec.MachineImages).To(HaveLen(1))
			Expect(cp.Spec.MachineImages[0].Name).To(Equal("gardenlinux-scibase-usi"))
		})
	})
})
