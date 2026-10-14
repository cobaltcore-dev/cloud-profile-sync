// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ironcore_test

import (
	"encoding/json"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/ironcore-dev/gardener-extension-provider-ironcore-metal/pkg/apis/metal/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/provider/ironcore"
)

// configure runs Configure, asserts it succeeded, and returns the resulting
// provider config so tests can thread it into a follow-up call.
func configure(p ossync.Provider, pc *runtime.RawExtension, versions []ossync.SourceImage) *runtime.RawExtension {
	GinkgoHelper()
	out, err := p.Configure(pc, versions)
	Expect(err).To(Succeed())
	return out
}

var _ = Describe("IroncoreProvider", func() {

	legacyProvider := &ironcore.IroncoreProvider{
		Registry:           "registry.io",
		Repository:         "repo",
		ImageName:          "test",
		EnableCapabilities: false,
	}

	capProvider := &ironcore.IroncoreProvider{
		Registry:           "registry.io",
		Repository:         "repo",
		ImageName:          "test",
		EnableCapabilities: true,
	}

	Describe("flag OFF (legacy format only)", func() {
		It("should add an image to the provider config", func() {
			var pc *runtime.RawExtension
			versions := []ossync.SourceImage{{Version: "v1.0.0", Architectures: []string{"amd64"}}}
			pc = configure(legacyProvider, pc, versions)

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(pc.Raw, &providerConfig)).To(Succeed())
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(1))
			Expect(providerConfig.MachineImages[0].Versions[0].Version).To(Equal("v1.0.0"))
			Expect(providerConfig.MachineImages[0].Versions[0].Image).To(Equal("registry.io/repo:v1.0.0"))
			Expect(providerConfig.MachineImages[0].Versions[0].Architecture).To(HaveValue(Equal("amd64")))
			Expect(providerConfig.MachineImages[0].Versions[0].CapabilityFlavors).To(BeEmpty())
		})

		It("should multiply out architectures", func() {
			var pc *runtime.RawExtension
			versions := []ossync.SourceImage{
				{Version: "v1.0.0", Architectures: []string{"amd64", "arm64"}},
			}
			pc = configure(legacyProvider, pc, versions)

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(pc.Raw, &providerConfig)).To(Succeed())

			amd64 := "amd64"
			arm64 := "arm64"
			Expect(providerConfig.MachineImages[0].Versions).To(ConsistOf([]v1alpha1.MachineImageVersion{
				{Version: "v1.0.0", Image: "registry.io/repo:v1.0.0", Architecture: &amd64},
				{Version: "v1.0.0", Image: "registry.io/repo:v1.0.0", Architecture: &arm64},
			}))
		})

		It("should not add duplicate images", func() {
			var pc *runtime.RawExtension
			versions := []ossync.SourceImage{
				{Version: "v1.0.0", Architectures: []string{"amd64"}},
				{Version: "v1.0.0", Architectures: []string{"arm64"}},
			}
			pc = configure(legacyProvider, pc, versions)
			pc = configure(legacyProvider, pc, versions)

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(pc.Raw, &providerConfig)).To(Succeed())
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(2))
		})

		It("should ignore Capabilities and CleanVersion", func() {
			var pc *runtime.RawExtension
			versions := []ossync.SourceImage{
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
			pc = configure(legacyProvider, pc, versions)

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(pc.Raw, &providerConfig)).To(Succeed())
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
			var pc *runtime.RawExtension
			versions := []ossync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  capabilities,
				},
			}
			pc = configure(capProvider, pc, versions)

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(pc.Raw, &providerConfig)).To(Succeed())
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
			var pc *runtime.RawExtension
			versions := []ossync.SourceImage{
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
			pc = configure(capProvider, pc, versions)

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(pc.Raw, &providerConfig)).To(Succeed())
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
			var pc *runtime.RawExtension
			versions := []ossync.SourceImage{
				{
					Version:       "2254.0.0-baremetal-sci-usi-amd64",
					CleanVersion:  "2254.0.0",
					Architectures: []string{"amd64"},
					Capabilities:  capabilities,
				},
			}
			pc = configure(capProvider, pc, versions)
			pc = configure(capProvider, pc, versions)

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(pc.Raw, &providerConfig)).To(Succeed())
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
			var pc *runtime.RawExtension
			versions := []ossync.SourceImage{
				{Version: "1877.0.0", Architectures: []string{"amd64"}},
			}
			pc = configure(capProvider, pc, versions)

			var providerConfig v1alpha1.CloudProfileConfig
			Expect(json.Unmarshal(pc.Raw, &providerConfig)).To(Succeed())
			Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(1))
			Expect(providerConfig.MachineImages[0].Versions[0].Version).To(Equal("1877.0.0"))
			Expect(providerConfig.MachineImages[0].Versions[0].CapabilityFlavors).To(BeEmpty())
		})
	})
})
