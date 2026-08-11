// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package openstack

import (
	"encoding/json"
	"testing"

	openstackv1alpha1 "github.com/gardener/gardener-extension-provider-openstack/pkg/apis/openstack/v1alpha1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

const (
	imageName   = "gardenlinux"
	testVersion = "2150.8.0"
	regionDE    = "eu-de-1"
	regionNL    = "eu-nl-1"
)

// Configure creates the image, version, and regions from an empty config.
func TestConfigureCreatesEntryFromEmpty(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	spec := specWithConfig(t, nil)

	err := p.Configure(spec, []ossync.SourceImage{
		{
			Version: testVersion,
			Regions: []ossync.RegionImage{
				{Region: regionDE, ID: "uuid-de-1"},
				{Region: regionNL, ID: "uuid-nl-1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, spec)
	img := findImage(cfg, imageName)
	if img == nil {
		t.Fatalf("machineImages entry %q not created: %+v", imageName, cfg.MachineImages)
	}
	v := findVersion(img, testVersion)
	if v == nil {
		t.Fatalf("version %s not created: %+v", testVersion, img.Versions)
	}
	if len(v.Regions) != 2 {
		t.Fatalf("got %d regions, want 2: %+v", len(v.Regions), v.Regions)
	}
}

// Configure merges into the existing image without dropping other versions.
func TestConfigureMergesIntoExistingImage(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	spec := specWithConfig(t, &openstackv1alpha1.CloudProfileConfig{
		MachineImages: []openstackv1alpha1.MachineImages{
			{
				Name: imageName,
				Versions: []openstackv1alpha1.MachineImageVersion{
					{
						Version: "2000.0.0",
						Regions: []openstackv1alpha1.RegionIDMapping{{Name: regionDE, ID: "old-uuid"}},
					},
				},
			},
		},
	})

	err := p.Configure(spec, []ossync.SourceImage{
		{Version: testVersion, Regions: []ossync.RegionImage{{Region: regionDE, ID: "new-uuid"}}},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, spec)
	if len(cfg.MachineImages) != 1 {
		t.Fatalf("got %d machineImages, want 1 (no duplicate entry): %+v", len(cfg.MachineImages), cfg.MachineImages)
	}
	img := findImage(cfg, imageName)
	if findVersion(img, "2000.0.0") == nil {
		t.Error("pre-existing version 2000.0.0 was dropped")
	}
	if findVersion(img, testVersion) == nil {
		t.Errorf("new version %s was not added", testVersion)
	}
}

// Configure does not duplicate an existing region or overwrite its ID.
func TestConfigureIsIdempotentOnRegions(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	spec := specWithConfig(t, &openstackv1alpha1.CloudProfileConfig{
		MachineImages: []openstackv1alpha1.MachineImages{
			{
				Name: imageName,
				Versions: []openstackv1alpha1.MachineImageVersion{
					{
						Version: testVersion,
						Regions: []openstackv1alpha1.RegionIDMapping{{Name: regionDE, ID: "existing-uuid"}},
					},
				},
			},
		},
	})

	// Re-apply the same region (with a different ID) plus a new one.
	err := p.Configure(spec, []ossync.SourceImage{
		{
			Version: testVersion,
			Regions: []ossync.RegionImage{
				{Region: regionDE, ID: "would-be-new-uuid"},
				{Region: regionNL, ID: "uuid-nl-1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, spec)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s missing", testVersion)
	}
	if len(v.Regions) != 2 {
		t.Fatalf("got %d regions, want 2 (%s must not be duplicated): %+v", len(v.Regions), regionDE, v.Regions)
	}
	for _, r := range v.Regions {
		if r.Name == regionDE && r.ID != "existing-uuid" {
			t.Errorf("%s ID = %q, want existing-uuid (existing region must not be overwritten)", regionDE, r.ID)
		}
	}
}

// Configure only touches the image matching p.ImageName.
func TestConfigureLeavesOtherImagesUntouched(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	spec := specWithConfig(t, &openstackv1alpha1.CloudProfileConfig{
		MachineImages: []openstackv1alpha1.MachineImages{
			{
				Name:     "coreos",
				Versions: []openstackv1alpha1.MachineImageVersion{{Version: "1.0.0"}},
			},
		},
	})

	err := p.Configure(spec, []ossync.SourceImage{
		{Version: testVersion, Regions: []ossync.RegionImage{{Region: regionDE, ID: "uuid"}}},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, spec)
	if len(cfg.MachineImages) != 2 {
		t.Fatalf("got %d machineImages, want 2 (coreos + gardenlinux): %+v", len(cfg.MachineImages), cfg.MachineImages)
	}
	coreos := findImage(cfg, "coreos")
	if coreos == nil || findVersion(coreos, "1.0.0") == nil {
		t.Error("unrelated image coreos was modified or dropped")
	}
}

// Configure returns an error for a malformed ProviderConfig.
func TestConfigureReturnsErrorOnInvalidConfig(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	spec := &gardencorev1beta1.CloudProfileSpec{
		ProviderConfig: &runtime.RawExtension{Raw: []byte("{not json")},
	}

	if err := p.Configure(spec, nil); err == nil {
		t.Fatal("Configure returned nil error for malformed ProviderConfig, want an error")
	}
}

// specWithConfig builds a CloudProfileSpec from cfg (nil yields no ProviderConfig).
func specWithConfig(t *testing.T, cfg *openstackv1alpha1.CloudProfileConfig) *gardencorev1beta1.CloudProfileSpec {
	t.Helper()
	spec := &gardencorev1beta1.CloudProfileSpec{}
	if cfg == nil {
		return spec
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	spec.ProviderConfig = &runtime.RawExtension{Raw: raw}
	return spec
}

// parseConfig unmarshals the ProviderConfig written back onto the spec.
func parseConfig(t *testing.T, spec *gardencorev1beta1.CloudProfileSpec) openstackv1alpha1.CloudProfileConfig {
	t.Helper()
	if spec.ProviderConfig == nil {
		t.Fatal("ProviderConfig is nil, want it to be set")
	}
	var cfg openstackv1alpha1.CloudProfileConfig
	if err := json.Unmarshal(spec.ProviderConfig.Raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	return cfg
}

// findImage returns the machineImages entry with the given name, or nil.
func findImage(cfg openstackv1alpha1.CloudProfileConfig, name string) *openstackv1alpha1.MachineImages {
	for i := range cfg.MachineImages {
		if cfg.MachineImages[i].Name == name {
			return &cfg.MachineImages[i]
		}
	}
	return nil
}

// findVersion returns the version entry with the given version string, or nil.
func findVersion(img *openstackv1alpha1.MachineImages, version string) *openstackv1alpha1.MachineImageVersion {
	for i := range img.Versions {
		if img.Versions[i].Version == version {
			return &img.Versions[i]
		}
	}
	return nil
}
