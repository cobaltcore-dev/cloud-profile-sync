// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"encoding/json"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/ironcore-dev/gardener-extension-provider-ironcore-metal/pkg/apis/metal/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

var _ = Describe("IroncoreProvider", func() {

	legacyProvider := &cloudprofilesync.IroncoreProvider{
		Registry:           "registry.io",
		Repository:         "repo",
		ImageName:          "test",
		EnableCapabilities: false,
	}

	capProvider := &cloudprofilesync.IroncoreProvider{
		Registry:           "registry.io",
		Repository:         "repo",
		ImageName:          "test",
		EnableCapabilities: true,
	}

	Describe("flag OFF (legacy format only)", func() {
		It("should add an image to the provider config", func() {
			var cpSpec gardencorev1beta1.CloudProfileSpec
			versions := []cloudprofilesync.SourceImage{{Version: "v1.0.0", Architectures: []string{"amd64"}}}
			Expect(legacyProvider.Configure(&cpSpec, versions)).To(Succeed())

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(1))
			Expect(providerConfig.MachineImages[0].Versions[0].Version).To(Equal("v1.0.0"))
			Expect(providerConfig.MachineImages[0].Versions[0].Image).To(Equal("registry.io/repo:v1.0.0"))
			Expect(providerConfig.MachineImages[0].Versions[0].Architecture).To(HaveValue(Equal("amd64")))
			Expect(providerConfig.MachineImages[0].Versions[0].CapabilityFlavors).To(BeEmpty())
		})

		It("should multiply out architectures", func() {
			var cpSpec gardencorev1beta1.CloudProfileSpec
			versions := []cloudprofilesync.SourceImage{
				{Version: "v1.0.0", Architectures: []string{"amd64", "arm64"}},
			}
			Expect(legacyProvider.Configure(&cpSpec, versions)).To(Succeed())

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &providerConfig)).To(Succeed())

			amd64 := "amd64"
			arm64 := "arm64"
			Expect(providerConfig.MachineImages[0].Versions).To(ConsistOf([]v1alpha1.MachineImageVersion{
				{Version: "v1.0.0", Image: "registry.io/repo:v1.0.0", Architecture: &amd64},
				{Version: "v1.0.0", Image: "registry.io/repo:v1.0.0", Architecture: &arm64},
			}))
		})

		It("should not add duplicate images", func() {
			var cpSpec gardencorev1beta1.CloudProfileSpec
			versions := []cloudprofilesync.SourceImage{
				{Version: "v1.0.0", Architectures: []string{"amd64"}},
				{Version: "v1.0.0", Architectures: []string{"arm64"}},
			}
			Expect(legacyProvider.Configure(&cpSpec, versions)).To(Succeed())
			Expect(legacyProvider.Configure(&cpSpec, versions)).To(Succeed())

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(2))
		})

		It("should ignore Capabilities and CleanVersion", func() {
			var cpSpec gardencorev1beta1.CloudProfileSpec
			versions := []cloudprofilesync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities: gardencorev1beta1.Capabilities{
						"architecture": {"amd64"},
						"feature":      {"sci", "_usi"},
					},
				},
			}
			Expect(legacyProvider.Configure(&cpSpec, versions)).To(Succeed())

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
			// Only the legacy flat entry — no CapabilityFlavors entry.
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(1))
			Expect(providerConfig.MachineImages[0].Versions[0].Version).To(Equal("2254.0.0-baremetal-sci-usi-amd64"))
			Expect(providerConfig.MachineImages[0].Versions[0].CapabilityFlavors).To(BeEmpty())
		})
	})

	Describe("flag ON (dual-write: legacy + CapabilityFlavors)", func() {
		capabilities := gardencorev1beta1.Capabilities{
			"architecture": {"amd64"},
			"feature":      {"sci", "_usi"},
		}

		It("should write both legacy flat entry and CapabilityFlavors entry", func() {
			var cpSpec gardencorev1beta1.CloudProfileSpec
			versions := []cloudprofilesync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  capabilities,
				},
			}
			Expect(capProvider.Configure(&cpSpec, versions)).To(Succeed())

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
			// Two entries: one legacy (full tag), one with CapabilityFlavors (clean version).
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(2))

			legacyEntry := providerConfig.MachineImages[0].Versions[0]
			Expect(legacyEntry.Version).To(Equal("2254.0.0-baremetal-sci-usi-amd64"))
			Expect(legacyEntry.Image).To(Equal("registry.io/repo:2254.0.0-baremetal-sci-usi-amd64"))
			Expect(legacyEntry.Architecture).To(HaveValue(Equal("amd64")))
			Expect(legacyEntry.CapabilityFlavors).To(BeEmpty())

			capEntry := providerConfig.MachineImages[0].Versions[1]
			Expect(capEntry.Version).To(Equal("2254.0.0"))
			Expect(capEntry.Image).To(BeEmpty())
			Expect(capEntry.Architecture).To(BeNil())
			Expect(capEntry.CapabilityFlavors).To(HaveLen(1))
			Expect(capEntry.CapabilityFlavors[0].Image).To(Equal("registry.io/repo:2254.0.0-baremetal-sci-usi-amd64"))
			Expect(capEntry.CapabilityFlavors[0].Capabilities).To(Equal(capabilities))
		})

		It("should group multiple flavors under one clean version entry", func() {
			var cpSpec gardencorev1beta1.CloudProfileSpec
			versions := []cloudprofilesync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities: gardencorev1beta1.Capabilities{
						"architecture": {"amd64"},
						"feature":      {"sci", "_usi"},
					},
				},
				{
					Version:       "2254.0.0-baremetal-sci-pxe-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities: gardencorev1beta1.Capabilities{
						"architecture": {"amd64"},
						"feature":      {"sci", "_pxe"},
					},
				},
			}
			Expect(capProvider.Configure(&cpSpec, versions)).To(Succeed())

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
			// Two legacy flat entries + one clean version entry with two flavors.
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(3))

			var cleanEntry *v1alpha1.MachineImageVersion
			for i := range providerConfig.MachineImages[0].Versions {
				if providerConfig.MachineImages[0].Versions[i].Version == "2254.0.0" {
					cleanEntry = &providerConfig.MachineImages[0].Versions[i]
				}
			}
			Expect(cleanEntry).ToNot(BeNil())
			Expect(cleanEntry.CapabilityFlavors).To(HaveLen(2))
		})

		It("should not add duplicate capability flavors on re-reconcile", func() {
			var cpSpec gardencorev1beta1.CloudProfileSpec
			versions := []cloudprofilesync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  capabilities,
				},
			}
			Expect(capProvider.Configure(&cpSpec, versions)).To(Succeed())
			Expect(capProvider.Configure(&cpSpec, versions)).To(Succeed())

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(2))

			var cleanEntry *v1alpha1.MachineImageVersion
			for i := range providerConfig.MachineImages[0].Versions {
				if providerConfig.MachineImages[0].Versions[i].Version == "2254.0.0" {
					cleanEntry = &providerConfig.MachineImages[0].Versions[i]
				}
			}
			Expect(cleanEntry.CapabilityFlavors).To(HaveLen(1))
		})

		It("should write only legacy entry for images without capabilities", func() {
			var cpSpec gardencorev1beta1.CloudProfileSpec
			versions := []cloudprofilesync.SourceImage{
				{Version: "1877.0.0", Architectures: []string{"amd64"}},
			}
			Expect(capProvider.Configure(&cpSpec, versions)).To(Succeed())

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(cpSpec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(1))
			Expect(providerConfig.MachineImages[0].Versions[0].Version).To(Equal("1877.0.0"))
			Expect(providerConfig.MachineImages[0].Versions[0].CapabilityFlavors).To(BeEmpty())
		})
	})
})
