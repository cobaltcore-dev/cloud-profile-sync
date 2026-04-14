// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers_test

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
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

type fakeOCISource struct{}

func (f *fakeOCISource) GetVersions(ctx context.Context) ([]cloudprofilesync.SourceImage, error) {
	return []cloudprofilesync.SourceImage{
		{Version: "1.0.0", Architectures: []string{"amd64"}},
		{Version: "1.0.1+abc", Architectures: []string{"amd64"}},
	}, nil
}

type fakeFactory struct{}

func (f *fakeFactory) Create(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error) {
	return &fakeOCISource{}, nil
}

func (m *mockOCIFactory) Create(params cloudprofilesync.OCIParams, insecure bool) (cloudprofilesync.Source, error) {
	return m.createFunc(params, insecure)
}

type fakeRegistryClient struct{}

func (f *fakeRegistryClient) GetTags(ctx context.Context, log logr.Logger, registry, repository string) (map[string]time.Time, error) {
	now := time.Now()
	return map[string]time.Time{
		"0.1.0":     now.Add(-48 * time.Hour),
		"1.0.0":     now.Add(-1 * time.Hour),
		"1.0.1+abc": now.Add(-48 * time.Hour),
	}, nil
}

var _ = Describe("The ManagedCloudProfile reconciler", func() {
	amd64 := "amd64"

	AfterEach(func(ctx SpecContext) {
		Eventually(func(g Gomega) {
			var mcpList v1alpha1.ManagedCloudProfileList
			err := k8sClient.List(ctx, &mcpList)
			g.Expect(err).To(Succeed())

			for _, mcp := range mcpList.Items {
				g.Expect(mcp.Status.Status).ToNot(Equal(v1alpha1.ReconcileStatus("InProgress")))
			}
		}).Should(Succeed())

		var mcpList v1alpha1.ManagedCloudProfileList
		err := k8sClient.List(ctx, &mcpList)
		Expect(err).To(Succeed())
		for _, mcp := range mcpList.Items {
			Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		}

		Eventually(func() int {
			var updated v1alpha1.ManagedCloudProfileList
			err := k8sClient.List(ctx, &updated)
			Expect(err).To(Succeed())
			return len(updated.Items)
		}).Should(Equal(0))

		var shootList gardenerv1beta1.ShootList
		err = k8sClient.List(ctx, &shootList)
		Expect(err).To(Succeed())
		for _, shoot := range shootList.Items {
			Expect(k8sClient.Delete(ctx, &shoot)).To(Succeed())
		}

		var cpList gardenerv1beta1.CloudProfileList
		err = k8sClient.List(ctx, &cpList)
		Expect(err).To(Succeed())
		for _, cp := range cpList.Items {
			Expect(k8sClient.Delete(ctx, &cp)).To(Succeed())
		}

		var secList corev1.SecretList
		err = k8sClient.List(ctx, &secList)
		Expect(err).To(Succeed())
		for _, sec := range secList.Items {
			if sec.Namespace == metav1.NamespaceDefault && sec.Name == "oci" {
				Expect(k8sClient.Delete(ctx, &sec)).To(Succeed())
			}
		}

		Eventually(func() int {
			total := 0

			var mcpList v1alpha1.ManagedCloudProfileList
			err := k8sClient.List(ctx, &mcpList)
			Expect(err).To(Succeed())
			total += len(mcpList.Items)

			var shootList gardenerv1beta1.ShootList
			err = k8sClient.List(ctx, &shootList)
			Expect(err).To(Succeed())
			total += len(shootList.Items)

			var cpList gardenerv1beta1.CloudProfileList
			err = k8sClient.List(ctx, &cpList)
			Expect(err).To(Succeed())
			total += len(cpList.Items)

			return total
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
						Repository: orasRepoName("repo"),
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
						Repository: orasRepoName("repo"),
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
		mcp.Name = "gc-mcp"
		usable := true

		oldVersion := "0.1.0"
		newVersion := "1.0.0"

		mcp.Spec.CloudProfile = v1alpha1.CloudProfileSpec{
			Regions: []gardenerv1beta1.Region{{Name: "foo"}},
			MachineImages: []gardenerv1beta1.MachineImage{
				{
					Name: "gc-image",
					Versions: []gardenerv1beta1.MachineImageVersion{
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: oldVersion}, Architectures: []string{"amd64"}},
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: newVersion}, Architectures: []string{"amd64"}},
					},
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

		mcp.Spec.MachineImageUpdates = []v1alpha1.MachineImageUpdate{
			{
				ImageName: "gc-image",
				Source: v1alpha1.MachineImageUpdateSource{
					OCI: &v1alpha1.MachineImageUpdateSourceOCI{
						Registry:   "keppel-fake",
						Repository: "account/repo",
						Insecure:   true,
					},
				},
			},
		}

		mcp.Spec.GarbageCollection = &v1alpha1.GarbageCollectionConfig{
			Enabled: true,
			MaxAge:  metav1.Duration{Duration: 24 * time.Hour},
		}

		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		shoot := &gardenerv1beta1.Shoot{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-shoot",
				Namespace: metav1.NamespaceDefault,
			},
			Spec: gardenerv1beta1.ShootSpec{
				CloudProfile: &gardenerv1beta1.CloudProfileReference{
					Name: mcp.Name,
				},
				Provider: gardenerv1beta1.Provider{
					Workers: []gardenerv1beta1.Worker{
						{
							Name: "worker1",
							Machine: gardenerv1beta1.Machine{
								Image: &gardenerv1beta1.ShootMachineImage{
									Name:    "gc-image",
									Version: &newVersion,
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shoot)).To(Succeed())

		reconciler := &controllers.Reconciler{
			Client:           k8sClient,
			OCISourceFactory: &fakeFactory{},
			RegistryProviderFunc: func(registry string) (controllers.RegistryClient, error) {
				return &fakeRegistryClient{}, nil
			},
		}

		_, err := reconciler.Reconcile(ctx, ctrl.Request{
			NamespacedName: client.ObjectKey{Name: mcp.Name},
		})
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) []string {
			var cp gardenerv1beta1.CloudProfile
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: mcp.Name}, &cp)).To(Succeed())

			var versions []string
			for _, mi := range cp.Spec.MachineImages {
				if mi.Name == "gc-image" {
					for _, v := range mi.Versions {
						versions = append(versions, v.Version)
					}
				}
			}
			return versions
		}, 10*time.Second, 200*time.Millisecond).
			Should(ConsistOf(newVersion))
	})

	It("preserves old machine image versions referenced by Shoot worker pools", func(ctx SpecContext) {
		version := "1.0.0"

		var cloudProfile gardenerv1beta1.CloudProfile
		cloudProfile.Name = "test-gc-preserve"
		cloudProfile.Spec.Regions = []gardenerv1beta1.Region{{Name: "foo"}}
		cloudProfile.Spec.MachineTypes = []gardenerv1beta1.MachineType{{Name: "baz"}}
		cloudProfile.Spec.MachineImages = []gardenerv1beta1.MachineImage{
			{
				Name: "preserve-image",
				Versions: []gardenerv1beta1.MachineImageVersion{
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{amd64}},
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{amd64}},
					{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "3.0.0"}, Architectures: []string{amd64}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, &cloudProfile)).To(Succeed())

		var shoot gardenerv1beta1.Shoot
		shoot.Name = "test-shoot-preserve"
		shoot.Namespace = metav1.NamespaceDefault
		shoot.Spec.CloudProfile = &gardenerv1beta1.CloudProfileReference{
			Name: cloudProfile.Name,
		}
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
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{amd64}},
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "2.0.0"}, Architectures: []string{amd64}},
						{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "3.0.0"}, Architectures: []string{amd64}},
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
						Repository: orasRepoName("repo"),
						Insecure:   true,
					},
				},
			},
		}

		mcp.Spec.GarbageCollection = &v1alpha1.GarbageCollectionConfig{
			Enabled: true,
			MaxAge:  metav1.Duration{Duration: time.Second},
		}

		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.FailedReconcileStatus))

		Eventually(func(g Gomega) []string {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&cloudProfile), &cloudProfile)).To(Succeed())

			var versions []string
			for _, v := range cloudProfile.Spec.MachineImages[0].Versions {
				versions = append(versions, v.Version)
			}
			return versions
		}).Should(ConsistOf("1.0.0", "2.0.0", "3.0.0", "1.0.1+abc"))
	})

	It("preserves machine image versions referenced by Shoot workers", func(ctx SpecContext) {
		version := "1.0.0"

		cp := &gardenerv1beta1.CloudProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-shoot-preserve",
			},
			Spec: gardenerv1beta1.CloudProfileSpec{
				Regions:      []gardenerv1beta1.Region{{Name: "foo"}},
				MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
				MachineImages: []gardenerv1beta1.MachineImage{
					{
						Name: "shoot-preserve-image",
						Versions: []gardenerv1beta1.MachineImageVersion{
							{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{amd64}},
							{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.1+abc"}, Architectures: []string{amd64}},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, cp)).To(Succeed())

		shoot := &gardenerv1beta1.Shoot{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-shoot",
				Namespace: metav1.NamespaceDefault,
			},
			Spec: gardenerv1beta1.ShootSpec{
				CloudProfile: &gardenerv1beta1.CloudProfileReference{Name: cp.Name},
				Provider: gardenerv1beta1.Provider{
					Workers: []gardenerv1beta1.Worker{
						{
							Name: "worker1",
							Machine: gardenerv1beta1.Machine{
								Image: &gardenerv1beta1.ShootMachineImage{
									Name:    "shoot-preserve-image",
									Version: &version,
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, shoot)).To(Succeed())

		mcp := &v1alpha1.ManagedCloudProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-shoot-preserve",
			},
			Spec: v1alpha1.ManagedCloudProfileSpec{
				CloudProfile: v1alpha1.CloudProfileSpec{
					Regions:      []gardenerv1beta1.Region{{Name: "foo"}},
					MachineTypes: []gardenerv1beta1.MachineType{{Name: "baz"}},
					MachineImages: []gardenerv1beta1.MachineImage{
						{
							Name: "shoot-preserve-image",
							Versions: []gardenerv1beta1.MachineImageVersion{
								{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.0"}, Architectures: []string{amd64}},
								{ExpirableVersion: gardenerv1beta1.ExpirableVersion{Version: "1.0.1+abc"}, Architectures: []string{amd64}},
							},
						},
					},
				},
				MachineImageUpdates: []v1alpha1.MachineImageUpdate{
					{
						ImageName: "shoot-preserve-image",
						Source: v1alpha1.MachineImageUpdateSource{
							OCI: &v1alpha1.MachineImageUpdateSourceOCI{
								Registry:   "keppel-fake",
								Repository: "account/repo",
								Insecure:   true,
							},
						},
					},
				},
				GarbageCollection: &v1alpha1.GarbageCollectionConfig{
					Enabled: true,
					MaxAge:  metav1.Duration{Duration: 0},
				},
			},
		}
		Expect(k8sClient.Create(ctx, mcp)).To(Succeed())

		reconciler := &controllers.Reconciler{
			Client:           k8sClient,
			OCISourceFactory: &fakeFactory{},
			RegistryProviderFunc: func(registry string) (controllers.RegistryClient, error) {
				return &fakeRegistryClient{}, nil
			},
		}

		req := ctrl.Request{
			NamespacedName: client.ObjectKey{Name: mcp.Name},
		}

		res, err := reconciler.Reconcile(context.Background(), req)
		Expect(err).ToNot(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(5 * time.Minute))

		Eventually(func(g Gomega) []string {
			updated := &gardenerv1beta1.CloudProfile{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cp), updated)).To(Succeed())

			var versions []string
			for _, mi := range updated.Spec.MachineImages {
				if mi.Name == "shoot-preserve-image" {
					for _, v := range mi.Versions {
						versions = append(versions, v.Version)
					}
				}
			}
			return versions
		}).Should(And(
			ContainElement("1.0.0"),
			Not(ContainElement("1.0.1+abc")),
		))
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
						Repository: orasRepoName("repository"),
						Insecure:   true,
					},
				},
			},
		}
		mcp.Spec.GarbageCollection = &v1alpha1.GarbageCollectionConfig{
			Enabled: true,
			MaxAge:  metav1.Duration{Duration: 3600000000000},
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
			HaveField("Message", ContainSubstring("Failed to apply CloudProfile: failed to initialize OCI source: invalid reference: invalid repository \"/registry/account/repository\"")),
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
			},
		}
		mcp.Spec.GarbageCollection = &v1alpha1.GarbageCollectionConfig{
			Enabled: true,
			MaxAge:  metav1.Duration{Duration: 3600000000000},
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
			HaveField("Message", ContainSubstring("Failed to apply CloudProfile: updating machine images failed: failed to retrieve image versions from OCI registry: simulated list error")),
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
						Repository: "repo/provider-config-image",
						Insecure:   true,
					},
				},
			},
		}
		mcp.Spec.GarbageCollection = &v1alpha1.GarbageCollectionConfig{
			Enabled: true,
			MaxAge:  metav1.Duration{Duration: 0},
		}
		Expect(k8sClient.Create(ctx, &mcp)).To(Succeed())

		Eventually(func(g Gomega) v1alpha1.ReconcileStatus {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&mcp), &mcp)).To(Succeed())
			return mcp.Status.Status
		}).Should(Equal(v1alpha1.FailedReconcileStatus))

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
		}).Should(ConsistOf(
			"repo/provider-config-image:1.0.0",
			"repo/provider-config-image:1.0.1+abc",
		))

		Expect(k8sClient.Delete(ctx, &mcp)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &cloudProfile)).To(Succeed())
	})

})
