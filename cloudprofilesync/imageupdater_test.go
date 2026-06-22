// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"encoding/json"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

var _ = Describe("filterImages", func() {
	// helper: run Update and return the versions written to spec.machineImages
	versions := func(ctx SpecContext, images []cloudprofilesync.SourceImage) []gardencorev1beta1.MachineImageVersion {
		mockSource.images = images
		updater := cloudprofilesync.ImageUpdater{
			Log:                GinkgoLogr,
			Source:             &mockSource,
			ImageName:          "test",
			EnableCapabilities: true,
		}
		var cpSpec gardencorev1beta1.CloudProfileSpec
		Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
		if len(cpSpec.MachineImages) == 0 {
			return nil
		}
		return cpSpec.MachineImages[0].Versions
	}

	It("invalid tag + no clean version: drops the image entirely", func(ctx SpecContext) {
		result := versions(ctx, []cloudprofilesync.SourceImage{
			{Version: "not-a-version", Architectures: []string{"amd64"}},
		})
		Expect(result).To(BeEmpty())
	})

	It("invalid tag + invalid clean version: drops the image entirely", func(ctx SpecContext) {
		result := versions(ctx, []cloudprofilesync.SourceImage{
			{Version: "not-a-version", CleanVersion: "also-not-a-version", Architectures: []string{"amd64"}},
		})
		Expect(result).To(BeEmpty())
	})

	It("invalid tag + valid clean version: NEW format only (no legacy entry)", func(ctx SpecContext) {
		result := versions(ctx, []cloudprofilesync.SourceImage{
			{
				Version:       "1877.9.2.0-metal-sci-pxe-amd64",
				CleanVersion:  "1877.9.2",
				Architectures: []string{"amd64"},
				Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_pxe"}},
			},
		})
		Expect(result).To(HaveLen(1))
		Expect(result[0].Version).To(Equal("1877.9.2"))
	})

	It("valid tag + valid clean version: BOTH formats", func(ctx SpecContext) {
		result := versions(ctx, []cloudprofilesync.SourceImage{
			{
				Version:       "2254.0.0-baremetal-sci-usi-amd64",
				CleanVersion:  "2254.0.0",
				Architectures: []string{"amd64"},
				Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_usi"}},
			},
		})
		Expect(result).To(HaveLen(2))
		versionStrings := []string{result[0].Version, result[1].Version}
		Expect(versionStrings).To(ContainElements("2254.0.0-baremetal-sci-usi-amd64", "2254.0.0"))
	})

	It("valid tag + no clean version: OLD format only", func(ctx SpecContext) {
		result := versions(ctx, []cloudprofilesync.SourceImage{
			{Version: "1921.0.0", Architectures: []string{"amd64"}},
		})
		Expect(result).To(HaveLen(1))
		Expect(result[0].Version).To(Equal("1921.0.0"))
	})

	It("valid tag + invalid clean version: BOTH formats with clean version normalized", func(ctx SpecContext) {
		result := versions(ctx, []cloudprofilesync.SourceImage{
			{
				Version:       "1921.0.0-metal-sci-usi-amd64",
				CleanVersion:  "1921.0",
				Architectures: []string{"amd64"},
				Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_usi"}},
			},
		})
		Expect(result).To(HaveLen(2))
		versionStrings := []string{result[0].Version, result[1].Version}
		Expect(versionStrings).To(ContainElements("1921.0.0-metal-sci-usi-amd64", "1921.0.0"))
	})

	It("valid tag + unparsable clean version: does not write clean version entry", func(ctx SpecContext) {
		result := versions(ctx, []cloudprofilesync.SourceImage{
			{
				Version:       "1921.0.0-metal-sci-usi-amd64",
				CleanVersion:  "not-a-version",
				Architectures: []string{"amd64"},
			},
		})
		Expect(result).To(HaveLen(1))
		Expect(result[0].Version).To(Equal("1921.0.0-metal-sci-usi-amd64"))
	})

	It("no architectures: drops the image entirely", func(ctx SpecContext) {
		result := versions(ctx, []cloudprofilesync.SourceImage{
			{Version: "1.0.0"},
		})
		Expect(result).To(BeEmpty())
	})
})

var _ = Describe("ImageUpdater", func() {
	Describe("flag OFF (default behavior)", func() {
		It("adds an image from the source to the CloudProfile spec", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{{Version: "1.0.0", Architectures: []string{"amd64"}}}
			updater := cloudprofilesync.ImageUpdater{
				Log:       logr.Discard(),
				Source:    &mockSource,
				ImageName: "test",
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
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
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(ConsistOf([]gardencorev1beta1.MachineImageVersion{
				{ExpirableVersion: gardencorev1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
				{ExpirableVersion: gardencorev1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{"arm64", "amd64"}},
			}))
		})

		It("updates an image from the source in the CloudProfile spec", func(ctx SpecContext) {
			cpSpec := gardencorev1beta1.CloudProfileSpec{
				MachineImages: []gardencorev1beta1.MachineImage{
					{Name: "test", Versions: []gardencorev1beta1.MachineImageVersion{
						{ExpirableVersion: gardencorev1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
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
			cpSpec := gardencorev1beta1.CloudProfileSpec{
				MachineImages: []gardencorev1beta1.MachineImage{
					{Name: "test", Versions: []gardencorev1beta1.MachineImageVersion{
						{ExpirableVersion: gardencorev1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					}},
					{Name: "other", Versions: []gardencorev1beta1.MachineImageVersion{
						{ExpirableVersion: gardencorev1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{"arm64"}},
					}},
				},
			}
			mockSource.images = []cloudprofilesync.SourceImage{{Version: "1.1.0", Architectures: []string{"arm64"}}}
			updater := cloudprofilesync.ImageUpdater{Log: GinkgoLogr, Source: &mockSource, ImageName: "test"}
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages).To(ConsistOf([]gardencorev1beta1.MachineImage{
				{Name: "test", Versions: []gardencorev1beta1.MachineImageVersion{
					{ExpirableVersion: gardencorev1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					{ExpirableVersion: gardencorev1beta1.ExpirableVersion{Version: "1.1.0"}, Architectures: []string{"arm64"}},
				}},
				{Name: "other", Versions: []gardencorev1beta1.MachineImageVersion{
					{ExpirableVersion: gardencorev1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{"arm64"}},
				}},
			}))
		})

		It("ignores CleanVersion when flag is OFF", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_usi"}},
				},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: false,
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("2254.0.0-baremetal-sci-usi-amd64"))
		})

		It("invokes the given provider", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{{Version: "1.0.0", Architectures: []string{"amd64"}}}
			updater := cloudprofilesync.ImageUpdater{
				Log:       GinkgoLogr,
				Source:    &mockSource,
				ImageName: "test",
				Provider:  &MockProvider{},
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			var fromProvider []cloudprofilesync.SourceImage
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &fromProvider)).To(Succeed())
			Expect(fromProvider).To(Equal(mockSource.images))
		})

		It("reflect inplace update ability to machineimage", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{{
				Version:       "1.0.0",
				Architectures: []string{"amd64"},
				Capabilities:  map[string]gardencorev1beta1.CapabilityValues{"feature": {cloudprofilesync.USIFeature}}},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:       logr.Discard(),
				Source:    &mockSource,
				ImageName: "test",
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("1.0.0"))
			Expect(cpSpec.MachineImages[0].Versions[0].InPlaceUpdates.Supported).To(BeTrue())
		})

	})

	Describe("flag ON (dual-write clean version)", func() {
		It("writes both full tag and clean version entries when CleanVersion differs", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_usi"}},
				},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(2))

			versions := cpSpec.MachineImages[0].Versions
			versionStrings := []string{versions[0].Version, versions[1].Version}
			Expect(versionStrings).To(ContainElements("2254.0.0-baremetal-sci-usi-amd64", "2254.0.0"))
			Expect(versions[0].InPlaceUpdates.Supported).To(BeTrue())
		})

		It("does not add a duplicate clean version entry on re-reconcile", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_usi"}},
				},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(2))
		})

		It("skips legacy spec entry for non-semver raw tag but still passes image to provider", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{
				{
					Version:       "1877.9.2.0-metal-sci-pxe-amd64-1877-9-2-6bb2b442",
					CleanVersion:  "1877.9.2",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "_pxe"}},
				},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
				Provider:           &MockProvider{},
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())

			// Non-semver raw tag must not appear in spec.machineImages — Gardener would reject it.
			// Only the clean version entry should be written.
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("1877.9.2"))

			// The raw tag must still reach the provider (capabilityFlavors).
			var fromProvider []cloudprofilesync.SourceImage
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &fromProvider)).To(Succeed())
			Expect(fromProvider).To(HaveLen(1))
			Expect(fromProvider[0].Version).To(Equal("1877.9.2.0-metal-sci-pxe-amd64-1877-9-2-6bb2b442"))
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
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("1877.0.0"))
		})

		It("reflect inplace update ability to machineimage", func(ctx SpecContext) {
			mockSource.images = []cloudprofilesync.SourceImage{{
				Version:       "1.0.0",
				Architectures: []string{"amd64"},
				Capabilities:  map[string]gardencorev1beta1.CapabilityValues{"feature": {cloudprofilesync.USIFeature}}},
			}
			updater := cloudprofilesync.ImageUpdater{
				Log:                logr.Discard(),
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("1.0.0"))
		})
	})
})
