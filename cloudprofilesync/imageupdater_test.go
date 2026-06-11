// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"encoding/json"

	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

var _ = Describe("ImageUpdater", func() {

	Describe("flag OFF (default behavior)", func() {
		It("adds an image from the source to the CloudProfile spec", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{{Version: "1.0.0", Architectures: []string{"amd64"}}}
			updater := cloudprofilesync.ImageUpdater{
				Log:       logr.Discard(),
				Source:    &mockSource,
				ImageName: "test",
			}
			var cpSpec v1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("1.0.0"))
		})

		It("adds multiple images from the source to the CloudProfile spec", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{Version: "1.0.0", Architectures: []string{"amd64"}},
				{Version: "2.0.0", Architectures: []string{"arm64", "amd64"}},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:       GinkgoLogr,
				Source:    &mockSource,
				ImageName: "test",
			}
			var cpSpec v1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(ConsistOf([]v1beta1.MachineImageVersion{
				{ExpirableVersion: v1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
				{ExpirableVersion: v1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{"arm64", "amd64"}},
			}))
		})

		It("updates an image from the source in the CloudProfile spec", func(ctx SpecContext) {
			cpSpec := v1beta1.CloudProfileSpec{
				MachineImages: []v1beta1.MachineImage{
					{Name: "test", Versions: []v1beta1.MachineImageVersion{
						{ExpirableVersion: v1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					}},
				},
			}
			mockSource.images = []cloudprofilesync.SourceImage{{Version: "2.0.0", Architectures: []string{"arm64"}}}
			updater := cloudprofilesync.ImageUpdater{Log: GinkgoLogr, Source: &mockSource, ImageName: "test"}
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(2))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("1.0.0"))
			Expect(cpSpec.MachineImages[0].Versions[1].Version).To(Equal("2.0.0"))
		})

		It("does not change unrelated images in the CloudProfile spec", func(ctx SpecContext) {
			cpSpec := v1beta1.CloudProfileSpec{
				MachineImages: []v1beta1.MachineImage{
					{Name: "test", Versions: []v1beta1.MachineImageVersion{
						{ExpirableVersion: v1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					}},
					{Name: "other", Versions: []v1beta1.MachineImageVersion{
						{ExpirableVersion: v1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{"arm64"}},
					}},
				},
			}
			mockSource.images = []cloudprofilesync.SourceImage{{Version: "1.1.0", Architectures: []string{"arm64"}}}
			updater := cloudprofilesync.ImageUpdater{Log: GinkgoLogr, Source: &mockSource, ImageName: "test"}
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages).To(ConsistOf([]v1beta1.MachineImage{
				{Name: "test", Versions: []v1beta1.MachineImageVersion{
					{ExpirableVersion: v1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					{ExpirableVersion: v1beta1.ExpirableVersion{Version: "1.1.0"}, Architectures: []string{"arm64"}},
				}},
				{Name: "other", Versions: []v1beta1.MachineImageVersion{
					{ExpirableVersion: v1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{"arm64"}},
				}},
			}))
		})

		It("ignores CleanVersion when flag is OFF", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{
					Version:      "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion: "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_usi"}},
				},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: false,
			}
			var cpSpec v1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("2254.0.0-baremetal-sci-usi-amd64"))
		})

		It("invokes the given provider", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{{Version: "1.0.0", Architectures: []string{"amd64"}}}
			updater := cloudprofilesync.ImageUpdater{
				Log:      GinkgoLogr,
				Source:   &mockSource,
				ImageName: "test",
				Provider: &MockProvider{},
			}
			var cpSpec v1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			var fromProvider []cloudprofilesync.SourceImage
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &fromProvider)).To(Succeed())
			Expect(fromProvider).To(Equal(mockSource.images))
		})
	})

	Describe("flag ON (dual-write clean version)", func() {
		It("writes both full tag and clean version entries when CleanVersion differs", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{
					Version:      "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion: "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_usi"}},
				},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec v1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(2))

			versions := cpSpec.MachineImages[0].Versions
			versionStrings := []string{versions[0].Version, versions[1].Version}
			Expect(versionStrings).To(ContainElements("2254.0.0-baremetal-sci-usi-amd64", "2254.0.0"))
		})

		It("does not add a duplicate clean version entry on re-reconcile", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{
					Version:      "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion: "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_usi"}},
				},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec v1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(2))
		})

		It("writes only full tag when CleanVersion is absent", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{Version: "1877.0.0", Architectures: []string{"amd64"}},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec v1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("1877.0.0"))
		})
	})
})
