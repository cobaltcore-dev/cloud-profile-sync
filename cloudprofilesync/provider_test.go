// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"encoding/json"

	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/ironcore-dev/gardener-extension-provider-ironcore-metal/pkg/apis/metal/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

var _ = Describe("IroncoreProvider", func() {

	provider := &cloudprofilesync.IroncoreProvider{
		Registry:   "registry.io",
		Repository: "repo",
		ImageName:  "test",
	}

	It("should add an image to the provider config", func() {
		var cloudProfile v1beta1.CloudProfile
		versions := []cloudprofilesync.SourceImage{{Version: "v1.0.0", Architectures: []string{"amd64"}}}
		Expect(provider.Configure(&cloudProfile, versions)).To(Succeed())
		Expect(cloudProfile.Spec.ProviderConfig).ToNot(BeNil())

		var providerConfig v1alpha1.CloudProfileConfig
		Expect(json.Unmarshal(cloudProfile.Spec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
		Expect(providerConfig.MachineImages).To(HaveLen(1))
		Expect(providerConfig.MachineImages[0].Name).To(Equal("test"))
		Expect(providerConfig.MachineImages[0].Versions).To(HaveLen(1))
		Expect(providerConfig.MachineImages[0].Versions[0].Version).To(Equal("v1.0.0"))
		Expect(providerConfig.MachineImages[0].Versions[0].Image).To(Equal("registry.io/repo:v1.0.0"))
		Expect(providerConfig.MachineImages[0].Versions[0].Architecture).To(HaveValue(Equal("amd64")))
	})

	It("should multiply out architectures", func() {
		var cloudProfile v1beta1.CloudProfile
		versions := []cloudprofilesync.SourceImage{
			{Version: "v1.0.0", Architectures: []string{"amd64", "arm64"}},
		}
		Expect(provider.Configure(&cloudProfile, versions)).To(Succeed())
		Expect(cloudProfile.Spec.ProviderConfig).ToNot(BeNil())

		var providerConfig v1alpha1.CloudProfileConfig
		Expect(json.Unmarshal(cloudProfile.Spec.ProviderConfig.Raw, &providerConfig)).To(Succeed())
		Expect(providerConfig.MachineImages).To(HaveLen(1))
		Expect(providerConfig.MachineImages[0].Name).To(Equal("test"))
		Expect(providerConfig.MachineImages[0].Versions).To(ConsistOf([]v1alpha1.MachineImageVersion{
			{
				Version:      "v1.0.0",
				Image:        "registry.io/repo:v1.0.0",
				Architecture: ptr.To("amd64"),
			},
			{
				Version:      "v1.0.0",
				Image:        "registry.io/repo:v1.0.0",
				Architecture: ptr.To("arm64"),
			},
		}))
	})

})
