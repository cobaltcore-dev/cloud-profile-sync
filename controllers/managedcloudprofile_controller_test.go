// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
)

var _ = Describe("The ManagedCloudProfile reconciler", func() {

	AfterEach(func(ctx SpecContext) {
		var mcpList v1alpha1.ManagedCloudProfileList
		Eventually(func(g Gomega) int {
			g.Expect(k8sClient.List(ctx, &mcpList)).To(Succeed())
			return len(mcpList.Items)
		}).Should(Equal(0))
		var cloudprofiles gardenerv1beta1.CloudProfileList
		Eventually(func(g Gomega) int {
			g.Expect(k8sClient.List(ctx, &cloudprofiles)).To(Succeed())
			return len(cloudprofiles.Items)
		}).Should(Equal(0))
	})

	It("should copy the spec of a ManagedCloudProfile to the respective CloudProfile", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-mcp"
		mcp.Spec.CloudProfile = gardenerv1beta1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "bar",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "0.3.0"}},
					},
				},
			},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = mcp.Name
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)
		}).Should(Succeed())
		Expect(cloudProfile.Spec).To(Equal(mcp.Spec.CloudProfile))

		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
	})

	It("invokes the image updater based on an image source", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-oci"
		mcp.Spec.CloudProfile = gardenerv1beta1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "the-image",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "0.3.0"}},
					},
				},
			},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImages = &v1alpha1.ManagedCloudProfileMachineImages{
			Source: v1alpha1.ManagedCloudProfileMachineImagesSource{
				OCI: &v1alpha1.ManagedCloudProfileMachineImagesSourceOCI{
					Registry:   registryAddr,
					Repository: "repo",
					Insecure:   true,
				},
			},
			ImageName: "the-image",
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = mcp.Name
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)
		}).Should(Succeed())

		Expect(cloudProfile.Spec.Regions).To(Equal(mcp.Spec.CloudProfile.Regions))
		Expect(cloudProfile.Spec.MachineTypes).To(Equal(mcp.Spec.CloudProfile.MachineTypes))
		mi := cloudProfile.Spec.MachineImages
		Expect(mi).To(HaveLen(1))
		Expect(mi[0].Name).To(Equal("the-image"))
		vers := mi[0].Versions
		Expect(vers).To(ContainElement(gardenerv1beta1.MachineImageVersion{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "0.3.0"}}))
		Expect(vers).To(ContainElement(gardenerv1beta1.MachineImageVersion{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}}))
		Expect(vers).To(ContainElement(gardenerv1beta1.MachineImageVersion{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.1+abc"}, Architectures: []string{"amd64"}}))

		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
	})

})
