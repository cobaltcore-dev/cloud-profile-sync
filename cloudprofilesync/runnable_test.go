// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync_test

import (
	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
)

var _ = Describe("Runnable", func() {

	It("adds an image from the source to the CloudProfile spec", func(ctx SpecContext) {
		mockSource.images = []cloudprofilesync.SourceImage{{Version: "v1.0.0", Architectures: []string{"amd64"}}}
		var cloudProfile v1beta1.CloudProfile
		Expect(runnable.CheckSource(ctx, &cloudProfile)).To(Succeed())
		Expect(cloudProfile.Spec.MachineImages).To(HaveLen(1))
		Expect(cloudProfile.Spec.MachineImages[0].Name).To(Equal("test"))
		Expect(cloudProfile.Spec.MachineImages[0].Versions).To(HaveLen(1))
		Expect(cloudProfile.Spec.MachineImages[0].Versions[0].Version).To(Equal("v1.0.0"))
		Expect(cloudProfile.Spec.MachineImages[0].Versions[0].Architectures).To(Equal([]string{"amd64"}))
	})

	It("adds multiple images from the source to the CloudProfile spec", func(ctx SpecContext) {
		mockSource.images = []cloudprofilesync.SourceImage{
			{Version: "v1.0.0", Architectures: []string{"amd64"}},
			{Version: "v2.0.0", Architectures: []string{"arm64", "amd64"}},
		}
		var cloudProfile v1beta1.CloudProfile
		Expect(runnable.CheckSource(ctx, &cloudProfile)).To(Succeed())
		Expect(cloudProfile.Spec.MachineImages).To(HaveLen(1))
		Expect(cloudProfile.Spec.MachineImages[0].Name).To(Equal("test"))
		Expect(cloudProfile.Spec.MachineImages[0].Versions).To(ConsistOf([]v1beta1.MachineImageVersion{
			{
				ExpirableVersion: v1beta1.ExpirableVersion{
					Version: "v1.0.0",
				},
				Architectures: []string{"amd64"},
			},
			{
				ExpirableVersion: v1beta1.ExpirableVersion{
					Version: "v2.0.0",
				},
				Architectures: []string{"arm64", "amd64"},
			},
		}))
	})

	It("updates an image from the source in the CloudProfile spec", func(ctx SpecContext) {
		cloudProfile := v1beta1.CloudProfile{
			Spec: v1beta1.CloudProfileSpec{
				MachineImages: []v1beta1.MachineImage{
					{
						Name: "test",
						Versions: []v1beta1.MachineImageVersion{
							{
								ExpirableVersion: v1beta1.ExpirableVersion{
									Version: "v1.0.0",
								},
								Architectures: []string{"amd64"},
							},
						},
					},
				},
			},
		}

		mockSource.images = []cloudprofilesync.SourceImage{{Version: "v2.0.0", Architectures: []string{"arm64"}}}
		Expect(runnable.CheckSource(ctx, &cloudProfile)).To(Succeed())
		Expect(cloudProfile.Spec.MachineImages).To(HaveLen(1))
		Expect(cloudProfile.Spec.MachineImages[0].Name).To(Equal("test"))
		Expect(cloudProfile.Spec.MachineImages[0].Versions).To(HaveLen(1))
		Expect(cloudProfile.Spec.MachineImages[0].Versions[0].Version).To(Equal("v2.0.0"))
		Expect(cloudProfile.Spec.MachineImages[0].Versions[0].Architectures).To(Equal([]string{"arm64"}))
	})

	It("does not change unrelated images in the CloudProfile spec", func(ctx SpecContext) {
		cloudProfile := v1beta1.CloudProfile{
			Spec: v1beta1.CloudProfileSpec{
				MachineImages: []v1beta1.MachineImage{
					{
						Name: "test",
						Versions: []v1beta1.MachineImageVersion{
							{
								ExpirableVersion: v1beta1.ExpirableVersion{
									Version: "v1.0.0",
								},
								Architectures: []string{"amd64"},
							},
						},
					},
					{
						Name: "other",
						Versions: []v1beta1.MachineImageVersion{
							{
								ExpirableVersion: v1beta1.ExpirableVersion{
									Version: "v2.0.0",
								},
								Architectures: []string{"arm64"},
							},
						},
					},
				},
			},
		}

		mockSource.images = []cloudprofilesync.SourceImage{{Version: "v1.1.0", Architectures: []string{"arm64"}}}
		Expect(runnable.CheckSource(ctx, &cloudProfile)).To(Succeed())
		Expect(cloudProfile.Spec.MachineImages).To(ConsistOf([]v1beta1.MachineImage{
			{
				Name: "test",
				Versions: []v1beta1.MachineImageVersion{
					{
						ExpirableVersion: v1beta1.ExpirableVersion{
							Version: "v1.1.0",
						},
						Architectures: []string{"arm64"},
					},
				},
			},
			{
				Name: "other",
				Versions: []v1beta1.MachineImageVersion{
					{
						ExpirableVersion: v1beta1.ExpirableVersion{
							Version: "v2.0.0",
						},
						Architectures: []string{"arm64"},
					},
				},
			},
		}))
	})

})
