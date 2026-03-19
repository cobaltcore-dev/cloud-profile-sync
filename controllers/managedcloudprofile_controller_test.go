// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"encoding/json"
	"errors"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	providercfg "github.com/ironcore-dev/gardener-extension-provider-ironcore-metal/pkg/apis/metal/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync"
	"github.com/cobaltcore-dev/cloud-profile-sync/controllers"
)

// fakeSource used to simulate GC list failures in tests
type fakeSource struct{}

func (f *fakeSource) GetVersions(ctx context.Context) ([]cloudprofilesync.SourceImage, error) {
	return nil, errors.New("simulated list error")
}

// mockOCIFactory implements controllers.OCISourceFactory for testing
type mockOCIFactory struct {
	createFunc func(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error)
}

func (m *mockOCIFactory) Create(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error) {
	return m.createFunc(params, insecure)
}

var _ = Describe("The ManagedCloudProfile reconciler", func() {
	amd64 := "amd64"
	version := "1.0.0"
	AfterEach(func(ctx SpecContext) {
		var mcpList v1alpha1.ManagedCloudProfileList
		Expect(k8sClient.List(ctx, &mcpList)).To(Succeed())
		for _, mcp := range mcpList.Items {
			Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		}

		var cpList gardenerv1beta1.CloudProfileList
		Expect(k8sClient.List(ctx, &cpList)).To(Succeed())
		for _, cp := range cpList.Items {
			Expect(k8sClient.Delete(ctx, &cp)).To(Succeed())
		}

		var shootList gardenerv1beta1.ShootList
		Expect(k8sClient.List(ctx, &shootList)).To(Succeed())
		for _, shoot := range shootList.Items {
			Expect(k8sClient.Delete(ctx, &shoot)).To(Succeed())
		}

		var secList corev1.SecretList
		Expect(k8sClient.List(ctx, &secList)).To(Succeed())
		for _, sec := range secList.Items {
			if sec.Namespace == metav1.NamespaceDefault && sec.Name == "oci" {
				Expect(k8sClient.Delete(ctx, &sec)).To(Succeed())
			}
		}

		Eventually(func(g Gomega) int {
			var updated v1alpha1.ManagedCloudProfileList
			g.Expect(k8sClient.List(ctx, &updated)).To(Succeed())
			return len(updated.Items)
		}).Should(Equal(0))

		Eventually(func(g Gomega) int {
			var updated gardenerv1beta1.ShootList
			g.Expect(k8sClient.List(ctx, &updated)).To(Succeed())
			return len(updated.Items)
		}).Should(Equal(0))

		Eventually(func(g Gomega) int {
			var updated corev1.SecretList
			g.Expect(k8sClient.List(ctx, &updated)).To(Succeed())
			count := 0
			for _, sec := range updated.Items {
				if sec.Namespace == metav1.NamespaceDefault && sec.Name == "oci" {
					count++
				}
			}
			return count
		}).Should(Equal(0))
	})

	It("should copy the spec of a ManagedCloudProfile to the respective CloudProfile", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-mcp"
		usable := true
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "bar",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{
							ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "0.3.0"},
							CRI:              []gardenerv1beta1.CRI{{Name: "containerd"}},
							Architectures:    []string{"amd64"},
						},
					},
					UpdateStrategy: ptr.To(gardenerv1beta1.UpdateStrategyMajor),
				},
			},
			MachineTypes: []gardenerv1beta1.MachineType{
				{
					Name:         "baz",
					Architecture: &amd64,
					Usable:       &usable,
				},
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.SucceededReconcileStatus))
		Expect(mcp.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", controllers.CloudProfileAppliedConditionType),
			HaveField("Status", metav1.ConditionTrue),
		)))
		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = mcp.Name
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)
		}).Should(Succeed())

		Expect(cloudProfile.Spec).To(Equal(controllers.CloudProfileSpecToGardener(&mcp.Spec.CloudProfile)))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
		Expect(mcp.Status.Status).To(Equal(v1alpha1.SucceededReconcileStatus))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
	})

	It("reports failure given an invalid cloudprofile", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-invalid"
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.FailedReconcileStatus))
		Expect(mcp.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", controllers.CloudProfileAppliedConditionType),
			HaveField("Status", metav1.ConditionFalse),
		)))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
	})

	It("invokes the image updater based on an image source", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-oci"
		usable := true
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineTypes: []gardenerv1beta1.MachineType{
				{
					Name:         "baz",
					Architecture: &amd64,
					Usable:       &usable,
				},
			},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   registryAddr,
						Repository: "repo",
						Insecure:   true,
					},
				},
				ImageName: "the-image",
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.SucceededReconcileStatus))
		Expect(mcp.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", controllers.CloudProfileAppliedConditionType),
			HaveField("Status", metav1.ConditionTrue),
		)))
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
		Expect(vers).To(ContainElement(gardenerv1beta1.MachineImageVersion{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}, CRI: []gardenerv1beta1.CRI{{Name: "containerd"}}}))
		Expect(vers).To(ContainElement(gardenerv1beta1.MachineImageVersion{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.1+abc"}, Architectures: []string{"amd64"}, CRI: []gardenerv1beta1.CRI{{Name: "containerd"}}}))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
		Expect(mcp.Status.Status).To(Equal(v1alpha1.SucceededReconcileStatus))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
	})

	It("fetches a secret for the OCI source", func(ctx SpecContext) {
		var secret corev1.Secret
		secret.Name = "oci"
		secret.Namespace = metav1.NamespaceDefault
		secret.Data = map[string][]byte{"password": []byte("pass")}
		Expect(k8sClient.Create(ctx, &secret)).To(Succeed())

		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-secret"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions:      []gardenerv1beta1.Region{{Name: "foo"}},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   registryAddr,
						Repository: "repo",
						Insecure:   true,
						Username:   "user",
						Password: v1alpha1.SecretReference{
							Name:      "oci",
							Namespace: metav1.NamespaceDefault,
							Key:       "password",
						},
					},
				},
				ImageName: "the-image",
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.SucceededReconcileStatus))
		Expect(mcp.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", controllers.CloudProfileAppliedConditionType),
			HaveField("Status", metav1.ConditionTrue),
		)))
		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = mcp.Name
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)
		}).Should(Succeed())
		Expect(cloudProfile.Spec.MachineImages).To(HaveLen(1))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &secret)).To(Succeed())
	})

	It("deletes old machine image versions not referenced by any Shoot", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-gc"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions:      []gardenerv1beta1.Region{{Name: "foo"}},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				ImageName: "gc-image",
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   registryAddr,
						Repository: "repo",
						Insecure:   true,
					},
				},
				GarbageCollection: &v1alpha1.GarbageCollectionConfig{
					Enabled: true,
					MaxAge:  metav1.Duration{Duration: 0},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.SucceededReconcileStatus))

		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = mcp.Name
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)).To(Succeed())

		Eventually(func(g Gomega) int {
			freshProfile := gardenerv1beta1.CloudProfile{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &freshProfile)).To(Succeed())
			if len(freshProfile.Spec.MachineImages) == 0 {
				return 0
			}
			for _, img := range freshProfile.Spec.MachineImages {
				if img.Name == "gc-image" {
					return len(img.Versions)
				}
			}
			return 0
		}, "10s").Should(Equal(0))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
	})

	It("preserves old machine image versions referenced by Shoot worker pools", func(ctx SpecContext) {
		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = "test-gc-preserve"
		cloudProfile.Spec.Regions = []gardenerv1beta1.Region{{Name: "foo"}}
		cloudProfile.Spec.MachineTypes = []gardenerv1beta1.MachineType{{Name: "baz"}}
		cloudProfile.Spec.MachineImages = []gardenerv1beta1.MachineImage{
			{
				Name: "preserve-image",
				Versions: []gardenerv1beta1.MachineImageVersion{
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{"amd64"}},
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "3.0.0"}, Architectures: []string{"amd64"}},
				},
			},
		}

		var cfg providercfg.CloudProfileConfig
		cfg.MachineImages = []providercfg.MachineImages{
			{
				Name: "preserve-image",
				Versions: []providercfg.MachineImageVersion{
					{Image: "repo/preserve-image:1.0.0"},
				},
			},
		}
		raw, err := json.Marshal(cfg)
		Expect(err).To(Succeed())
		cloudProfile.Spec.ProviderConfig = &runtime.RawExtension{Raw: raw}
		Expect(k8sClient.Create(ctx, &cloudProfile)).To(Succeed())

		var shoot gardenerv1beta1.Shoot
		shoot.Name = "test-shoot-preserve"
		shoot.Namespace = metav1.NamespaceDefault
		shoot.Spec.CloudProfile = &gardenerv1beta1.CloudProfileReference{Name: cloudProfile.Name}
		shoot.Spec.Provider.Workers = []gardenerv1beta1.Worker{
			{
				Name: "worker1",
				Machine: gardenerv1beta1.Machine{
					Image: &gardenerv1beta1.ShootMachineImage{
						Name:    "preserve-image",
						Version: &version,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &shoot)).To(Succeed())

		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-gc-preserve"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "preserve-image",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{"amd64"}},
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "3.0.0"}, Architectures: []string{"amd64"}},
					},
				},
			},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				ImageName: "preserve-image",
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   registryAddr,
						Repository: "repo",
						Insecure:   true,
					},
				},
				GarbageCollection: &v1alpha1.GarbageCollectionConfig{
					Enabled: true,
					MaxAge:  metav1.Duration{Duration: 0},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.SucceededReconcileStatus))

		Eventually(func(g Gomega) []string {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)).To(Succeed())
			if len(cloudProfile.Spec.MachineImages) == 0 {
				return []string{}
			}
			versions := []string{}
			for _, v := range cloudProfile.Spec.MachineImages[0].Versions {
				versions = append(versions, v.Version)
			}
			return versions
		}).Should(ContainElement("1.0.0"))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &shoot)).To(Succeed())
	})

	It("preserves machine image versions referenced by Shoot workers", func(ctx SpecContext) {
		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = "test-gc-shoot-preserve"
		cloudProfile.Spec.Regions = []gardenerv1beta1.Region{{Name: "foo"}}
		cloudProfile.Spec.MachineTypes = []gardenerv1beta1.MachineType{{Name: "baz"}}
		cloudProfile.Spec.MachineImages = []gardenerv1beta1.MachineImage{
			{
				Name: "shoot-preserve-image",
				Versions: []gardenerv1beta1.MachineImageVersion{
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.1+abc"}, Architectures: []string{"amd64"}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &cloudProfile)).To(Succeed())

		var shoot gardenerv1beta1.Shoot
		shoot.Name = "test-shoot"
		shoot.Namespace = metav1.NamespaceDefault
		shoot.Spec.CloudProfile = &gardenerv1beta1.CloudProfileReference{Name: cloudProfile.Name}
		shoot.Spec.Provider.Workers = []gardenerv1beta1.Worker{
			{
				Name: "worker1",
				Machine: gardenerv1beta1.Machine{
					Image: &gardenerv1beta1.ShootMachineImage{
						Name:    "shoot-preserve-image",
						Version: &version,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &shoot)).To(Succeed())

		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-gc-shoot-preserve"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "shoot-preserve-image",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.1+abc"}, Architectures: []string{"amd64"}},
					},
				},
			},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				ImageName: "shoot-preserve-image",
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   registryAddr,
						Repository: "repo",
						Insecure:   true,
					},
				},
				GarbageCollection: &v1alpha1.GarbageCollectionConfig{
					Enabled: true,
					MaxAge:  metav1.Duration{Duration: 0},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.SucceededReconcileStatus))

		Eventually(func(g Gomega) []string {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)).To(Succeed())
			if len(cloudProfile.Spec.MachineImages) == 0 {
				return []string{}
			}
			versions := []string{}
			for _, v := range cloudProfile.Spec.MachineImages[0].Versions {
				versions = append(versions, v.Version)
			}
			return versions
		}).Should(And(
			ContainElement("1.0.0"),
			Not(ContainElement("1.0.1+abc")),
		))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &shoot)).To(Succeed())
	})

	It("handles missing credential for GC OCI source", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-gc-cred-error"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions:      []gardenerv1beta1.Region{{Name: "foo"}},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				ImageName: "test-image",
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   registryAddr,
						Repository: "repo",
						Insecure:   true,
						Password: v1alpha1.SecretReference{
							Name:      "nonexistent-secret",
							Namespace: metav1.NamespaceDefault,
							Key:       "password",
						},
					},
				},
				GarbageCollection: &v1alpha1.GarbageCollectionConfig{
					Enabled: true,
					MaxAge:  metav1.Duration{Duration: 3600000000000},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.FailedReconcileStatus))

		Expect(mcp.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", controllers.CloudProfileAppliedConditionType),
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Message", ContainSubstring("failed to get secret")),
		)))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
	})

	It("handles invalid OCI registry for GC", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-gc-invalid-registry"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions:      []gardenerv1beta1.Region{{Name: "foo"}},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				ImageName: "test-image",
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   "invalid://registry",
						Repository: "repo",
						Insecure:   true,
					},
				},
				GarbageCollection: &v1alpha1.GarbageCollectionConfig{
					Enabled: true,
					MaxAge:  metav1.Duration{Duration: 3600000000000},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.FailedReconcileStatus))

		Expect(mcp.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", controllers.CloudProfileAppliedConditionType),
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Reason", "ApplyFailed"),
			HaveField("Message", ContainSubstring("failed to initialize oci source")),
		)))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
	})

	It("reports failure when GC version listing errors occur", func(ctx SpecContext) {
		old := reconciler.OCISourceFactory
		defer func() { reconciler.OCISourceFactory = old }()
		reconciler.OCISourceFactory = &mockOCIFactory{
			createFunc: func(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error) {
				return &fakeSource{}, nil
			},
		}

		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-gc-list-error"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions:      []gardenerv1beta1.Region{{Name: "foo"}},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				ImageName: "test-image",
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   registryAddr,
						Repository: "repo",
						Insecure:   true,
					},
				},
				GarbageCollection: &v1alpha1.GarbageCollectionConfig{
					Enabled: true,
					MaxAge:  metav1.Duration{Duration: 3600000000000},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.FailedReconcileStatus))

		Expect(mcp.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", controllers.CloudProfileAppliedConditionType),
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Reason", "ApplyFailed"),
			HaveField("Message", ContainSubstring("failed to retrieve images version")),
		)))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
	})

	It("skips GC when no source is configured", func(ctx SpecContext) {
		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-gc-no-source"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "test-image",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					},
				},
			},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		var cp gardenerv1beta1.CloudProfile
		Eventually(func(g Gomega) error {
			return k8sClient.Get(ctx, client.ObjectKey{Name: mcp.Name}, &cp)
		}).Should(Succeed())

		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: mcp.Name}, &cp)).To(Succeed())
		Expect(cp.Spec.MachineImages).To(HaveLen(1))
		Expect(cp.Spec.MachineImages[0].Versions).To(HaveLen(1))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
	})

	It("reports failure when CloudProfile is already owned by another controller", func(ctx SpecContext) {
		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = "test-owned"
		usable := true
		cloudProfile.Spec = gardenerv1beta1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "existing-region"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "existing-image",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					},
				},
			},
			MachineTypes: []gardenerv1beta1.MachineType{
				{Name: "existing-type", Architecture: &amd64, Usable: &usable},
			},
		}
		cloudProfile.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion: "v1",
				Kind:       "Dummy",
				Name:       "dummy-owner",
				UID:        "dummy-uid",
			},
		}
		Expect(k8sClient.Create(ctx, &cloudProfile)).To(Succeed())

		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-owned"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions:      []gardenerv1beta1.Region{{Name: "foo"}},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.FailedReconcileStatus))
		Expect(mcp.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", controllers.CloudProfileAppliedConditionType),
			HaveField("Status", metav1.ConditionFalse),
		)))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
	})

	It("updates ProviderConfig when garbage collecting machine image versions", func(ctx SpecContext) {
		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = "test-gc-provider-config"
		cloudProfile.Spec.Regions = []gardenerv1beta1.Region{{Name: "foo"}}
		cloudProfile.Spec.MachineTypes = []gardenerv1beta1.MachineType{{Name: "baz"}}
		cloudProfile.Spec.MachineImages = []gardenerv1beta1.MachineImage{
			{
				Name: "provider-config-image",
				Versions: []gardenerv1beta1.MachineImageVersion{
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.1+abc"}, Architectures: []string{"amd64"}},
				},
			},
		}

		var cfg providercfg.CloudProfileConfig
		cfg.MachineImages = []providercfg.MachineImages{
			{
				Name: "provider-config-image",
				Versions: []providercfg.MachineImageVersion{
					{Image: "repo/provider-config-image:1.0.0"},
					{Image: "repo/provider-config-image:1.0.1+abc"},
				},
			},
		}
		raw, err := json.Marshal(cfg)
		Expect(err).To(Succeed())
		cloudProfile.Spec.ProviderConfig = &runtime.RawExtension{Raw: raw}
		Expect(k8sClient.Create(ctx, &cloudProfile)).To(Succeed())

		var mcp v1alpha1.ManagedCloudProfile
		mcp.Name = "test-gc-provider-config"
		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "provider-config-image",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{"amd64"}},
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.1+abc"}, Architectures: []string{"amd64"}},
					},
				},
			},
			MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
		}
		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				ImageName: "provider-config-image",
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   registryAddr,
						Repository: "repo",
						Insecure:   true,
					},
				},
				GarbageCollection: &v1alpha1.GarbageCollectionConfig{
					Enabled: true,
					MaxAge:  metav1.Duration{Duration: 0},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.SucceededReconcileStatus))

		Eventually(func(g Gomega) []string {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)).To(Succeed())
			if cloudProfile.Spec.ProviderConfig == nil {
				return []string{}
			}
			var updatedCfg providercfg.CloudProfileConfig
			if err := json.Unmarshal(cloudProfile.Spec.ProviderConfig.Raw, &updatedCfg); err != nil {
				return []string{}
			}
			for _, img := range updatedCfg.MachineImages {
				if img.Name == "provider-config-image" {
					images := make([]string, len(img.Versions))
					for i, v := range img.Versions {
						images[i] = v.Image
					}
					return images
				}
			}
			return []string{}
		}).Should(BeEmpty())

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
	})

})
