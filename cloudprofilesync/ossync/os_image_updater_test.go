// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ossync_test

import (
	"encoding/json"
	"time"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

var _ = Describe("filterImages", func() {
	// helper: run Update and return the versions written to spec.machineImages
	versions := func(ctx SpecContext, images []ossync.SourceImage) []gardencorev1beta1.MachineImageVersion {
		mockSource.images = images
		updater := ossync.ImageUpdater{
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
		result := versions(ctx, []ossync.SourceImage{
			{Version: "not-a-version", Architectures: []string{"amd64"}},
		})
		Expect(result).To(BeEmpty())
	})

	It("invalid tag + invalid clean version: drops the image entirely", func(ctx SpecContext) {
		result := versions(ctx, []ossync.SourceImage{
			{Version: "not-a-version", CleanVersion: "also-not-a-version", Architectures: []string{"amd64"}},
		})
		Expect(result).To(BeEmpty())
	})

	It("invalid tag + valid clean version: NEW format only (no legacy entry)", func(ctx SpecContext) {
		result := versions(ctx, []ossync.SourceImage{
			{
				Version:       "1877.9.2.0-metal-sci-pxe-amd64",
				CleanVersion:  "1877.9.2",
				Architectures: []string{"amd64"},
				Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "pxe"}},
			},
		})
		Expect(result).To(HaveLen(1))
		Expect(result[0].Version).To(Equal("1877.9.2"))
	})

	It("valid tag + valid clean version: BOTH formats", func(ctx SpecContext) {
		result := versions(ctx, []ossync.SourceImage{
			{
				Version:       "2254.0.0-baremetal-sci-usi-amd64",
				CleanVersion:  "2254.0.0",
				Architectures: []string{"amd64"},
				Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
			},
		})
		Expect(result).To(HaveLen(2))
		versionStrings := []string{result[0].Version, result[1].Version}
		Expect(versionStrings).To(ContainElements("2254.0.0-baremetal-sci-usi-amd64", "2254.0.0"))
	})

	It("valid tag + no clean version: OLD format only", func(ctx SpecContext) {
		result := versions(ctx, []ossync.SourceImage{
			{Version: "1921.0.0", Architectures: []string{"amd64"}},
		})
		Expect(result).To(HaveLen(1))
		Expect(result[0].Version).To(Equal("1921.0.0"))
	})

	It("valid tag + invalid clean version: BOTH formats with clean version normalized", func(ctx SpecContext) {
		result := versions(ctx, []ossync.SourceImage{
			{
				Version:       "1921.0.0-metal-sci-usi-amd64",
				CleanVersion:  "1921.0",
				Architectures: []string{"amd64"},
				Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
			},
		})
		Expect(result).To(HaveLen(2))
		versionStrings := []string{result[0].Version, result[1].Version}
		Expect(versionStrings).To(ContainElements("1921.0.0-metal-sci-usi-amd64", "1921.0.0"))
	})

	It("valid tag + unparsable clean version: does not write clean version entry", func(ctx SpecContext) {
		result := versions(ctx, []ossync.SourceImage{
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
		result := versions(ctx, []ossync.SourceImage{
			{Version: "1.0.0"},
		})
		Expect(result).To(BeEmpty())
	})
})

var _ = Describe("ImageUpdater", func() {
	Describe("flag OFF (default behavior)", func() {
		It("adds an image from the source to the CloudProfile spec", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{{Version: "1.0.0", Architectures: []string{"amd64"}}}
			updater := ossync.ImageUpdater{
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
			mockSource.images = []ossync.SourceImage{
				{Version: "1.0.0", Architectures: []string{"amd64"}},
				{Version: "2.0.0", Architectures: []string{"arm64", "amd64"}},
			}
			updater := ossync.ImageUpdater{
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
			mockSource.images = []ossync.SourceImage{{Version: "2.0.0", Architectures: []string{"arm64"}}}
			updater := ossync.ImageUpdater{Log: GinkgoLogr, Source: &mockSource, ImageName: "test"}
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
			mockSource.images = []ossync.SourceImage{{Version: "1.1.0", Architectures: []string{"arm64"}}}
			updater := ossync.ImageUpdater{Log: GinkgoLogr, Source: &mockSource, ImageName: "test"}
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
			mockSource.images = []ossync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
				},
			}
			updater := ossync.ImageUpdater{
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
			mockSource.images = []ossync.SourceImage{{Version: "1.0.0", Architectures: []string{"amd64"}}}
			updater := ossync.ImageUpdater{
				Log:       GinkgoLogr,
				Source:    &mockSource,
				ImageName: "test",
				Provider:  &MockProvider{},
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			var fromProvider []ossync.SourceImage
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &fromProvider)).To(Succeed())
			Expect(fromProvider).To(Equal(mockSource.images))
		})

		It("in-place update support", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{{
				Version:              "1.0.0",
				Architectures:        []string{"amd64"},
				SupportInPlaceUpdate: true,
			}}
			updater := ossync.ImageUpdater{
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
		// cpSpecWithCaps returns a CloudProfileSpec with MachineCapabilities covering
		// the keys used by the test source images. Required because filterCapabilities
		// returns nil when MachineCapabilities is empty.
		cpSpecWithCaps := func() gardencorev1beta1.CloudProfileSpec {
			return gardencorev1beta1.CloudProfileSpec{
				MachineCapabilities: []gardencorev1beta1.CapabilityDefinition{
					{Name: "architecture", Values: []string{"amd64", "arm64"}},
					{Name: "feature_set", Values: []string{"sci", "usi", "pxe", "scibase"}},
				},
			}
		}

		It("sets CapabilityFlavors when CleanVersion equals Version (semver tag with matching annotation)", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{
				{
					Version:       "2254.0.0",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
				},
			}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			cpSpec := cpSpecWithCaps()
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())

			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			v := cpSpec.MachineImages[0].Versions[0]
			Expect(v.Version).To(Equal("2254.0.0"))
			Expect(v.CapabilityFlavors).To(HaveLen(1))
			Expect(v.CapabilityFlavors[0].Capabilities).To(Equal(
				gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
			))
		})

		It("sets CapabilityFlavors on the clean version entry", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
				},
			}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			cpSpec := cpSpecWithCaps()
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())

			versions := cpSpec.MachineImages[0].Versions
			var cleanEntry *gardencorev1beta1.MachineImageVersion
			for i := range versions {
				if versions[i].Version == "2254.0.0" {
					cleanEntry = &versions[i]
					break
				}
			}
			Expect(cleanEntry).NotTo(BeNil())
			Expect(cleanEntry.CapabilityFlavors).To(HaveLen(1))
			Expect(cleanEntry.CapabilityFlavors[0].Capabilities).To(Equal(
				gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
			))
		})

		It("accumulates multiple flavors under the same clean version entry", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
				},
				{
					Version:       "2254.0.0-baremetal-sci-pxe-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "pxe"}},
				},
			}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			cpSpec := cpSpecWithCaps()
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())

			versions := cpSpec.MachineImages[0].Versions
			var cleanEntry *gardencorev1beta1.MachineImageVersion
			for i := range versions {
				if versions[i].Version == "2254.0.0" {
					cleanEntry = &versions[i]
					break
				}
			}
			Expect(cleanEntry).NotTo(BeNil())
			Expect(cleanEntry.CapabilityFlavors).To(HaveLen(2))
			flavors := []gardencorev1beta1.Capabilities{
				cleanEntry.CapabilityFlavors[0].Capabilities,
				cleanEntry.CapabilityFlavors[1].Capabilities,
			}
			Expect(flavors).To(ConsistOf(
				gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
				gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "pxe"}},
			))
		})

		It("does not append duplicate flavors on re-reconcile", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
				},
			}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			cpSpec := cpSpecWithCaps()
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())

			versions := cpSpec.MachineImages[0].Versions
			var cleanEntry *gardencorev1beta1.MachineImageVersion
			for i := range versions {
				if versions[i].Version == "2254.0.0" {
					cleanEntry = &versions[i]
					break
				}
			}
			Expect(cleanEntry).NotTo(BeNil())
			Expect(cleanEntry.CapabilityFlavors).To(HaveLen(1))
		})

		It("does not set CapabilityFlavors when Capabilities is nil", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
				},
			}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())

			versions := cpSpec.MachineImages[0].Versions
			var cleanEntry *gardencorev1beta1.MachineImageVersion
			for i := range versions {
				if versions[i].Version == "2254.0.0" {
					cleanEntry = &versions[i]
					break
				}
			}
			Expect(cleanEntry).NotTo(BeNil())
			Expect(cleanEntry.CapabilityFlavors).To(BeEmpty())
		})

		It("writes both full tag and clean version entries when CleanVersion differs", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{
				{
					Version:              "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:         "2254.0.0",
					Architectures:        []string{"amd64"},
					Capabilities:         gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
					SupportInPlaceUpdate: true,
				},
			}
			updater := ossync.ImageUpdater{
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
			mockSource.images = []ossync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "usi"}},
				},
			}
			updater := ossync.ImageUpdater{
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
			mockSource.images = []ossync.SourceImage{
				{
					Version:       "1877.9.2.0-metal-sci-pxe-amd64-1877-9-2-6bb2b442",
					CleanVersion:  "1877.9.2",
					Architectures: []string{"amd64"},
					Capabilities:  gardencorev1beta1.Capabilities{"architecture": {"amd64"}, "feature_set": {"sci", "pxe"}},
				},
			}
			updater := ossync.ImageUpdater{
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
			var fromProvider []ossync.SourceImage
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &fromProvider)).To(Succeed())
			Expect(fromProvider).To(HaveLen(1))
			Expect(fromProvider[0].Version).To(Equal("1877.9.2.0-metal-sci-pxe-amd64-1877-9-2-6bb2b442"))
		})

		It("writes only full tag when CleanVersion is absent", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{
				{Version: "1877.0.0", Architectures: []string{"amd64"}},
			}
			updater := ossync.ImageUpdater{
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

		It("in-place update support", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{{
				Version:              "1.0.0",
				CleanVersion:         "1.1",
				Architectures:        []string{"amd64"},
				SupportInPlaceUpdate: true,
			}}
			updater := ossync.ImageUpdater{
				Log:                logr.Discard(),
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(2))
			Expect(cpSpec.MachineImages[0].Versions[0].Version).To(Equal("1.0.0"))
			Expect(cpSpec.MachineImages[0].Versions[0].InPlaceUpdates).NotTo(BeNil())
			Expect(cpSpec.MachineImages[0].Versions[0].InPlaceUpdates.Supported).To(BeTrue())
			Expect(cpSpec.MachineImages[0].Versions[1].Version).To(Equal("1.1.0"))
			Expect(cpSpec.MachineImages[0].Versions[1].InPlaceUpdates).NotTo(BeNil())
			Expect(cpSpec.MachineImages[0].Versions[1].InPlaceUpdates.Supported).To(BeTrue())
		})
	})

	Describe("capability filtering via MachineCapabilities", func() {
		It("writes no CapabilityFlavors when MachineCapabilities is empty", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{{
				Version:       "2254.0.0",
				CleanVersion:  "2254.0.0",
				Architectures: []string{"amd64"},
				Capabilities: gardencorev1beta1.Capabilities{
					"architecture": {"amd64"},
					"feature_set":  {"sci", "usi"},
					"hypervisor":   {"kvm"},
				},
			}}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			v := cpSpec.MachineImages[0].Versions[0]
			Expect(v.CapabilityFlavors).To(BeEmpty())
		})

		It("drops capability values absent from MachineCapabilities", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{{
				Version:       "2254.0.0",
				CleanVersion:  "2254.0.0",
				Architectures: []string{"amd64"},
				Capabilities: gardencorev1beta1.Capabilities{
					"architecture": {"amd64"},
					"feature_set":  {"sci", "usi", "rescue"},
				},
			}}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			cpSpec := gardencorev1beta1.CloudProfileSpec{
				MachineCapabilities: []gardencorev1beta1.CapabilityDefinition{
					{Name: "architecture", Values: []string{"amd64", "arm64"}},
					{Name: "feature_set", Values: []string{"sci", "usi"}},
				},
			}
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			v := cpSpec.MachineImages[0].Versions[0]
			Expect(v.CapabilityFlavors).To(HaveLen(1))
			Expect(v.CapabilityFlavors[0].Capabilities).To(Equal(gardencorev1beta1.Capabilities{
				"architecture": {"amd64"},
				"feature_set":  {"sci", "usi"}, // "rescue" dropped — not in MachineCapabilities
			}))
		})

		It("populates all declared capability keys into capabilityFlavors", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{{
				Version:       "2254.0.0",
				CleanVersion:  "2254.0.0",
				Architectures: []string{"amd64"},
				Capabilities: gardencorev1beta1.Capabilities{
					"architecture": {"amd64"},
					"feature_set":  {"sci", "usi"},
					"hypervisor":   {"kvm"},
				},
			}}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			cpSpec := gardencorev1beta1.CloudProfileSpec{
				MachineCapabilities: []gardencorev1beta1.CapabilityDefinition{
					{Name: "architecture", Values: []string{"amd64", "arm64"}},
					{Name: "feature_set", Values: []string{"sci", "usi", "scibase"}},
					{Name: "hypervisor", Values: []string{"kvm", "xen"}},
				},
			}
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			v := cpSpec.MachineImages[0].Versions[0]
			Expect(v.CapabilityFlavors).To(HaveLen(1))
			Expect(v.CapabilityFlavors[0].Capabilities).To(Equal(gardencorev1beta1.Capabilities{
				"architecture": {"amd64"},
				"feature_set":  {"sci", "usi"},
				"hypervisor":   {"kvm"},
			}))
		})

		It("drops a capability key entirely when none of its values are in MachineCapabilities", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{{
				Version:       "2254.0.0",
				CleanVersion:  "2254.0.0",
				Architectures: []string{"amd64"},
				Capabilities: gardencorev1beta1.Capabilities{
					"architecture": {"amd64"},
					"feature_set":  {"sci", "usi"},
					"hypervisor":   {"kvm"},
				},
			}}
			updater := ossync.ImageUpdater{
				Log:                GinkgoLogr,
				Source:             &mockSource,
				ImageName:          "test",
				EnableCapabilities: true,
			}
			cpSpec := gardencorev1beta1.CloudProfileSpec{
				MachineCapabilities: []gardencorev1beta1.CapabilityDefinition{
					{Name: "architecture", Values: []string{"amd64", "arm64"}},
					{Name: "feature_set", Values: []string{"sci", "usi"}},
					// hypervisor not declared → key dropped entirely
				},
			}
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			v := cpSpec.MachineImages[0].Versions[0]
			Expect(v.CapabilityFlavors).To(HaveLen(1))
			Expect(v.CapabilityFlavors[0].Capabilities).To(Equal(gardencorev1beta1.Capabilities{
				"architecture": {"amd64"},
				"feature_set":  {"sci", "usi"},
			}))
			Expect(v.CapabilityFlavors[0].Capabilities).NotTo(HaveKey("hypervisor"))
		})
	})

	Describe("expiration", func() {
		deprecated := gardencorev1beta1.ClassificationDeprecated

		newUpdater := func() ossync.ImageUpdater {
			return ossync.ImageUpdater{Log: GinkgoLogr, Source: &mockSource, ImageName: "test"}
		}

		It("keeps the existing expiration date for a deprecated version (never overwrites)", func(ctx SpecContext) {
			existing := metav1.NewTime(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC))
			cpSpec := gardencorev1beta1.CloudProfileSpec{
				MachineImages: []gardencorev1beta1.MachineImage{
					{Name: "test", Versions: []gardencorev1beta1.MachineImageVersion{
						{ExpirableVersion: gardencorev1beta1.ExpirableVersion{
							Version:        "1.0.0",
							Classification: &deprecated, //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
							ExpirationDate: &existing,   //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
						}, Architectures: []string{"amd64"}},
					}},
				},
			}
			mockSource.images = []ossync.SourceImage{
				{Version: "1.0.0", Architectures: []string{"amd64"}, Classification: &deprecated},
			}
			updater := newUpdater()
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].ExpirationDate).To(Equal(&existing)) //nolint:staticcheck // legacy field; Lifecycle needs the VersionClassificationLifecycle feature gate
		})

		It("uses the source's expiration date for a new deprecated version", func(ctx SpecContext) {
			fromSource := metav1.NewTime(time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC))
			mockSource.images = []ossync.SourceImage{
				{Version: "1.0.0", Architectures: []string{"amd64"}, Classification: &deprecated, ExpirationDate: &fromSource},
			}
			updater := newUpdater()
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].ExpirationDate).To(Equal(&fromSource)) //nolint:staticcheck // legacy field; Lifecycle needs the VersionClassificationLifecycle feature gate
		})

		It("does not set an expiration date for a non-deprecated version", func(ctx SpecContext) {
			mockSource.images = []ossync.SourceImage{
				{Version: "1.0.0", Architectures: []string{"amd64"}},
			}
			updater := newUpdater()
			var cpSpec gardencorev1beta1.CloudProfileSpec
			Expect(updater.Update(ctx, &cpSpec)).To(Succeed())
			Expect(cpSpec.MachineImages[0].Versions).To(HaveLen(1))
			Expect(cpSpec.MachineImages[0].Versions[0].ExpirationDate).To(BeNil()) //nolint:staticcheck // legacy field; Lifecycle needs the VersionClassificationLifecycle feature gate
		})
	})
})
