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
	region1     = "region1"
	region2     = "region2"
)

// Configure creates the image, version, and regions from an empty config.
func TestConfigureCreatesEntryFromEmpty(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	spec := specWithConfig(t, nil)

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version: testVersion,
			Regions: []ossync.RegionImage{
				{Region: region1, ID: "uuid-r1"},
				{Region: region2, ID: "uuid-r2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
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
						Regions: []openstackv1alpha1.RegionIDMapping{{Name: region1, ID: "old-uuid"}},
					},
				},
			},
		},
	})

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{Version: testVersion, Regions: []ossync.RegionImage{{Region: region1, ID: "new-uuid"}}},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
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

// Configure does not duplicate an existing region but updates its ID so a
// rebuilt image (same version, new UUID) replaces the stale mapping.
func TestConfigureUpdatesExistingRegionID(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	spec := specWithConfig(t, &openstackv1alpha1.CloudProfileConfig{
		MachineImages: []openstackv1alpha1.MachineImages{
			{
				Name: imageName,
				Versions: []openstackv1alpha1.MachineImageVersion{
					{
						Version: testVersion,
						Regions: []openstackv1alpha1.RegionIDMapping{{Name: region1, ID: "stale-uuid"}},
					},
				},
			},
		},
	})

	// Re-apply the same region (with a new ID) plus a new one.
	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version: testVersion,
			Regions: []ossync.RegionImage{
				{Region: region1, ID: "rebuilt-uuid"},
				{Region: region2, ID: "uuid-r2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s missing", testVersion)
	}
	if len(v.Regions) != 2 {
		t.Fatalf("got %d regions, want 2 (%s must not be duplicated): %+v", len(v.Regions), region1, v.Regions)
	}
	for _, r := range v.Regions {
		if r.Name == region1 && r.ID != "rebuilt-uuid" {
			t.Errorf("%s ID = %q, want rebuilt-uuid (stale mapping must be updated)", region1, r.ID)
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

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{Version: testVersion, Regions: []ossync.RegionImage{{Region: region1, ID: "uuid"}}},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	if len(cfg.MachineImages) != 2 {
		t.Fatalf("got %d machineImages, want 2 (coreos + gardenlinux): %+v", len(cfg.MachineImages), cfg.MachineImages)
	}
	coreos := findImage(cfg, "coreos")
	if coreos == nil || findVersion(coreos, "1.0.0") == nil {
		t.Error("unrelated image coreos was modified or dropped")
	}
}

// Regions fed in reverse alphabetical order must be stored in sorted order.
func TestConfigureRegionsSortedAlphabetically(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	spec := specWithConfig(t, nil)

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version: testVersion,
			Regions: []ossync.RegionImage{
				{Region: region2, ID: "uuid-r2"},
				{Region: region1, ID: "uuid-r1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if len(v.Regions) != 2 {
		t.Fatalf("got %d regions, want 2", len(v.Regions))
	}
	if v.Regions[0].Name != region1 || v.Regions[1].Name != region2 {
		t.Errorf("regions not sorted alphabetically: got [%s, %s], want [%s, %s]",
			v.Regions[0].Name, v.Regions[1].Name, region1, region2)
	}
}

// Configure returns an error for a malformed ProviderConfig.
func TestConfigureReturnsErrorOnInvalidConfig(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	pc := &runtime.RawExtension{Raw: []byte("{not json")}

	if _, err := p.Configure(pc, nil); err == nil {
		t.Fatal("Configure returned nil error for malformed ProviderConfig, want an error")
	}
}

// Configure with an empty versions slice must write back the config unchanged and not error.
func TestConfigureEmptyVersions(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName}
	existing := &openstackv1alpha1.CloudProfileConfig{
		MachineImages: []openstackv1alpha1.MachineImages{
			{Name: imageName, Versions: []openstackv1alpha1.MachineImageVersion{{Version: testVersion}}},
		},
	}
	spec := specWithConfig(t, existing)

	if err := p.Configure(spec, []ossync.SourceImage{}); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, spec)
	if len(cfg.MachineImages) != 1 || findVersion(findImage(cfg, imageName), testVersion) == nil {
		t.Error("existing config was modified by empty versions slice")
	}
}

// specWithConfig builds a CloudProfileSpec from cfg (nil yields no ProviderConfig).
func specWithConfig(t *testing.T, cfg *openstackv1alpha1.CloudProfileConfig) *runtime.RawExtension {
	t.Helper()
	if cfg == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return &runtime.RawExtension{Raw: raw}
}

// parseConfig unmarshals the ProviderConfig returned by Configure.
func parseConfig(t *testing.T, pc *runtime.RawExtension) openstackv1alpha1.CloudProfileConfig {
	t.Helper()
	if pc == nil {
		t.Fatal("ProviderConfig is nil, want it to be set")
	}
	var cfg openstackv1alpha1.CloudProfileConfig
	if err := json.Unmarshal(pc.Raw, &cfg); err != nil {
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

func TestUpsertRegion(t *testing.T) {
	tests := []struct {
		name    string
		regions []openstackv1alpha1.RegionIDMapping
		region  string
		id      string
		want    []openstackv1alpha1.RegionIDMapping
	}{
		{
			name:   "append to nil slice",
			region: region1, id: "uuid-1",
			want: []openstackv1alpha1.RegionIDMapping{{Name: region1, ID: "uuid-1"}},
		},
		{
			name:    "append new region to existing",
			regions: []openstackv1alpha1.RegionIDMapping{{Name: region1, ID: "uuid-1"}},
			region:  region2, id: "uuid-2",
			want: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: region2, ID: "uuid-2"},
			},
		},
		{
			name:    "update existing region ID",
			regions: []openstackv1alpha1.RegionIDMapping{{Name: region1, ID: "stale"}},
			region:  region1, id: "new-uuid",
			want: []openstackv1alpha1.RegionIDMapping{{Name: region1, ID: "new-uuid"}},
		},
		{
			name: "update first of multiple, second unchanged",
			regions: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "stale"},
				{Name: region2, ID: "uuid-2"},
			},
			region: region1, id: "new-uuid",
			want: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "new-uuid"},
				{Name: region2, ID: "uuid-2"},
			},
		},
		{
			name: "update last of multiple, first unchanged",
			regions: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: region2, ID: "stale"},
			},
			region: region2, id: "new-uuid",
			want: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: region2, ID: "new-uuid"},
			},
		},
		{
			name: "update middle of multiple, neighbours unchanged",
			regions: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: "region3", ID: "stale"},
				{Name: region2, ID: "uuid-3"},
			},
			region: "region3", id: "new-uuid",
			want: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: "region3", ID: "new-uuid"},
				{Name: region2, ID: "uuid-3"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := upsertRegion(tc.regions, tc.region, tc.id)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d regions, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, r := range got {
				if r.Name != tc.want[i].Name || r.ID != tc.want[i].ID {
					t.Errorf("regions[%d] = {%s, %s}, want {%s, %s}",
						i, r.Name, r.ID, tc.want[i].Name, tc.want[i].ID)
				}
			}
		})
	}
}

func TestSortRegions(t *testing.T) {
	tests := []struct {
		name  string
		input []openstackv1alpha1.RegionIDMapping
		want  []openstackv1alpha1.RegionIDMapping
	}{
		{
			name:  "nil slice",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty slice",
			input: []openstackv1alpha1.RegionIDMapping{},
			want:  []openstackv1alpha1.RegionIDMapping{},
		},
		{
			name: "already sorted",
			input: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: region2, ID: "uuid-2"},
			},
			want: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: region2, ID: "uuid-2"},
			},
		},
		{
			name: "reverse order",
			input: []openstackv1alpha1.RegionIDMapping{
				{Name: region2, ID: "uuid-2"},
				{Name: region1, ID: "uuid-1"},
			},
			want: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: region2, ID: "uuid-2"},
			},
		},
		{
			name: "same name tie-break by ID",
			input: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-b"},
				{Name: region1, ID: "uuid-a"},
			},
			want: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-a"},
				{Name: region1, ID: "uuid-b"},
			},
		},
		{
			name: "three regions out of order",
			input: []openstackv1alpha1.RegionIDMapping{
				{Name: "region3", ID: "uuid-3"},
				{Name: region1, ID: "uuid-1"},
				{Name: region2, ID: "uuid-2"},
			},
			want: []openstackv1alpha1.RegionIDMapping{
				{Name: region1, ID: "uuid-1"},
				{Name: region2, ID: "uuid-2"},
				{Name: "region3", ID: "uuid-3"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sortRegions(tc.input)
			if len(tc.input) != len(tc.want) {
				t.Fatalf("got %d regions, want %d", len(tc.input), len(tc.want))
			}
			for i, r := range tc.input {
				if r.Name != tc.want[i].Name || r.ID != tc.want[i].ID {
					t.Errorf("regions[%d] = {%s, %s}, want {%s, %s}",
						i, r.Name, r.ID, tc.want[i].Name, tc.want[i].ID)
				}
			}
		})
	}
}

// EnableCapabilities flag and nil Capabilities control whether legacy regions or
// capabilityFlavors are written. validator.admission-openstack.extensions.gardener.cloud
// forbids both fields on the same version entry.
func TestConfigureCapabilitiesFlag(t *testing.T) {
	caps := gardencorev1beta1.Capabilities{"architecture": {"amd64"}}
	tests := []struct {
		name               string
		enableCapabilities bool
		capabilities       gardencorev1beta1.Capabilities
		wantRegionCount    int
		wantFlavorCount    int
	}{
		{
			name:               "flag off ignores capabilities",
			enableCapabilities: false,
			capabilities:       caps,
			wantRegionCount:    1,
			wantFlavorCount:    0,
		},
		{
			name:               "flag on writes flavors only",
			enableCapabilities: true,
			capabilities:       caps,
			wantRegionCount:    0,
			wantFlavorCount:    1,
		},
		{
			name:               "flag on nil capabilities falls through to legacy",
			enableCapabilities: true,
			capabilities:       nil, // image predates capability annotations
			wantRegionCount:    1,
			wantFlavorCount:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: tc.enableCapabilities}
			spec := specWithConfig(t, nil)

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version:      testVersion,
			CleanVersion: testVersion,
			Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}},
			Regions:      []ossync.RegionImage{{Region: region1, ID: "uuid-r1"}},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s not found", testVersion)
	}
	if len(v.Regions) != 1 {
		t.Errorf("got %d legacy regions, want 1", len(v.Regions))
	}
	if len(v.CapabilityFlavors) != 0 {
		t.Errorf("got %d capabilityFlavors, want 0 (EnableCapabilities is false)", len(v.CapabilityFlavors))
	}
}

// EnableCapabilities=true: must write both the legacy Regions list and a CapabilityFlavors entry
// on the same version entry, with all source regions present in the flavor.
func TestConfigureCapabilitiesFlagOn(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: true}
	spec := specWithConfig(t, nil)
	caps := gardencorev1beta1.Capabilities{"architecture": {"amd64"}}

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version:      testVersion,
			CleanVersion: testVersion,
			Capabilities: caps,
			Regions: []ossync.RegionImage{
				{Region: region1, ID: "uuid-r1"},
				{Region: region2, ID: "uuid-r2"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s not found", testVersion)
	}
	// Legacy Regions must still be present for backwards compatibility.
	if len(v.Regions) != 2 {
		t.Errorf("got %d legacy regions, want 2", len(v.Regions))
	}
	if len(v.CapabilityFlavors) != 1 {
		t.Fatalf("got %d capabilityFlavors, want 1: %+v", len(v.CapabilityFlavors), v.CapabilityFlavors)
	}
	flavor := v.CapabilityFlavors[0]
	arch := flavor.Capabilities["architecture"]
	if len(arch) != 1 || arch[0] != "amd64" {
		t.Errorf("flavor Capabilities[architecture] = %v, want [amd64]", arch)
	}
	// The flavor must carry both regions, not just the first.
	if len(flavor.Regions) != 2 {
		t.Errorf("got %d flavor regions, want 2: %+v", len(flavor.Regions), flavor.Regions)
	}
}

// Flavor regions fed in reverse alphabetical order must be stored in sorted order.
func TestConfigureCapabilitiesFlavorRegionsSortedAlphabetically(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: true}
	spec := specWithConfig(t, nil)

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version:      testVersion,
			CleanVersion: testVersion,
			Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}},
			Regions: []ossync.RegionImage{
				{Region: region2, ID: "uuid-r2"},
				{Region: region1, ID: "uuid-r1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if len(v.CapabilityFlavors) != 1 {
		t.Fatalf("got %d flavors, want 1", len(v.CapabilityFlavors))
	}
	regions := v.CapabilityFlavors[0].Regions
	if len(regions) != 2 {
		t.Fatalf("got %d flavor regions, want 2", len(regions))
	}
	if regions[0].Name != region1 || regions[1].Name != region2 {
		t.Errorf("flavor regions not sorted alphabetically: got [%s, %s], want [%s, %s]",
			regions[0].Name, regions[1].Name, region1, region2)
	}
}

// Re-applying the same input must not duplicate CapabilityFlavors or their region entries.
func TestConfigureCapabilitiesIdempotent(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: true}
	spec := specWithConfig(t, nil)
	versions := []ossync.SourceImage{
		{
			Version:      testVersion,
			CleanVersion: testVersion,
			Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}},
			Regions:      []ossync.RegionImage{{Region: region1, ID: "uuid-r1"}},
		},
	}

	pc, err := p.Configure(spec, versions)
	if err != nil {
		t.Fatalf("first Configure: %v", err)
	}
	pc, err = p.Configure(pc, versions)
	if err != nil {
		t.Fatalf("second Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s not found", testVersion)
	}
	if len(v.Regions) != 0 {
		t.Errorf("got %d legacy regions after re-reconcile, want 0 (capabilities enabled)", len(v.Regions))
	}
	if len(v.CapabilityFlavors) != 1 {
		t.Errorf("got %d capabilityFlavors after re-reconcile, want 1 (must not duplicate)", len(v.CapabilityFlavors))
	}
}

// A rebuilt image keeps the same version string but gets a new Glance UUID.
// The flavor's region entry must be updated in place, not duplicated.
func TestConfigureCapabilitiesUpdatesFlavorRegionUUID(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: true}
	spec := specWithConfig(t, nil)
	caps := gardencorev1beta1.Capabilities{"architecture": {"amd64"}}

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{Version: testVersion, CleanVersion: testVersion, Capabilities: caps,
			Regions: []ossync.RegionImage{{Region: region1, ID: "original-uuid"}}},
	})
	if err != nil {
		t.Fatalf("first Configure: %v", err)
	}
	pc, err = p.Configure(pc, []ossync.SourceImage{
		{Version: testVersion, CleanVersion: testVersion, Capabilities: caps,
			Regions: []ossync.RegionImage{{Region: region1, ID: "rebuilt-uuid"}}},
	})
	if err != nil {
		t.Fatalf("second Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s not found", testVersion)
	}
	if len(v.CapabilityFlavors) != 1 {
		t.Fatalf("got %d capabilityFlavors, want 1", len(v.CapabilityFlavors))
	}
	if id := v.CapabilityFlavors[0].Regions[0].ID; id != "rebuilt-uuid" {
		t.Errorf("flavor region ID = %q, want rebuilt-uuid (stale UUID must be replaced)", id)
	}
}

// Source image with nil Capabilities must not write any flavor even when the flag is on.
func TestConfigureCapabilitiesNilCapabilitiesSkipsFlavor(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: true}
	spec := specWithConfig(t, nil)

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version: testVersion,
			Regions: []ossync.RegionImage{{Region: region1, ID: "uuid-r1"}},
			// Capabilities deliberately nil — image predates capability annotations.
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s not found", testVersion)
	}
	if len(v.CapabilityFlavors) != 0 {
		t.Errorf("got %d capabilityFlavors, want 0 (nil Capabilities must not produce a flavor)", len(v.CapabilityFlavors))
	}
}

// Two source images with different capability sets on the same version must produce
// two separate CapabilityFlavors entries. This verifies that ossync.CapabilitiesEqual
// is wired correctly as the dedup key — a bug there would silently collapse both
// architectures into one flavor.
func TestConfigureCapabilitiesMultipleFlavorsOnSameVersion(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: true}
	spec := specWithConfig(t, nil)

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version:      testVersion,
			CleanVersion: testVersion,
			Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}},
			Regions:      []ossync.RegionImage{{Region: region1, ID: "amd64-r1-uuid"}},
		},
		{
			Version:      testVersion,
			CleanVersion: testVersion,
			Capabilities: gardencorev1beta1.Capabilities{"architecture": {"arm64"}},
			Regions:      []ossync.RegionImage{{Region: region1, ID: "arm64-r1-uuid"}},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s not found", testVersion)
	}
	if len(v.CapabilityFlavors) != 2 {
		t.Fatalf("got %d capabilityFlavors, want 2 (one per architecture): %+v",
			len(v.CapabilityFlavors), v.CapabilityFlavors)
	}
	if len(v.Regions) != 0 {
		t.Errorf("got %d legacy regions, want 0 (must not coexist with capabilityFlavors)", len(v.Regions))
	}

	for _, wantArch := range []string{"amd64", "arm64"} {
		var found *openstackv1alpha1.MachineImageFlavor
		for i := range v.CapabilityFlavors {
			if arch := v.CapabilityFlavors[i].Capabilities["architecture"]; len(arch) == 1 && arch[0] == wantArch {
				found = &v.CapabilityFlavors[i]
				break
			}
		}
		if found == nil {
			t.Errorf("no flavor found for architecture=%s", wantArch)
			continue
		}
		if len(found.Regions) != 1 || found.Regions[0].Name != region1 {
			t.Errorf("flavor %s: unexpected regions %+v", wantArch, found.Regions)
		}
		if wantID := wantArch + "-r1-uuid"; found.Regions[0].ID != wantID {
			t.Errorf("flavor %s: region ID = %q, want %q", wantArch, found.Regions[0].ID, wantID)
		}
	}
}

// Two source images with different versions must each get their own flavor — flavors
// must not bleed across version entries.
func TestConfigureCapabilitiesMultipleVersionsWithFlavors(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: true}
	spec := specWithConfig(t, nil)

	pc, err := p.Configure(spec, []ossync.SourceImage{
		{
			Version:      "2254.0.0",
			CleanVersion: "2254.0.0",
			Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}},
			Regions:      []ossync.RegionImage{{Region: region1, ID: "uuid-2254-r1"}},
		},
		{
			Version:      "2255.0.0",
			CleanVersion: "2255.0.0",
			Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}},
			Regions:      []ossync.RegionImage{{Region: region1, ID: "uuid-2255-r1"}},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, pc)
	img := findImage(cfg, imageName)

	for _, tc := range []struct{ version, wantID string }{
		{"2254.0.0", "uuid-2254-r1"},
		{"2255.0.0", "uuid-2255-r1"},
	} {
		v := findVersion(img, tc.version)
		if v == nil {
			t.Fatalf("version %s not found", tc.version)
		}
		if len(v.CapabilityFlavors) != 1 {
			t.Errorf("version %s: got %d flavors, want 1 (flavors must not bleed across versions)",
				tc.version, len(v.CapabilityFlavors))
			continue
		}
		if len(v.Regions) != 0 {
			t.Errorf("version %s: got %d legacy regions, want 0 (must not coexist with capabilityFlavors)", tc.version, len(v.Regions))
		}
		if id := v.CapabilityFlavors[0].Regions[0].ID; id != tc.wantID {
			t.Errorf("version %s: region ID = %q, want %q", tc.version, id, tc.wantID)
		}
	}
}

// A version that already carries legacy regions (written before capabilities were enabled)
// must have those regions cleared when capabilities are enabled on the next reconcile.
func TestConfigureCapabilitiesClearsLegacyRegionsOnMigration(t *testing.T) {
	p := &OpenStackProvider{ImageName: imageName, EnableCapabilities: true}
	spec := specWithConfig(t, &openstackv1alpha1.CloudProfileConfig{
		MachineImages: []openstackv1alpha1.MachineImages{
			{
				Name: imageName,
				Versions: []openstackv1alpha1.MachineImageVersion{
					{
						Version: testVersion,
						Regions: []openstackv1alpha1.RegionIDMapping{
							{Name: region1, ID: "legacy-uuid"},
						},
					},
				},
			},
		},
	})

	err := p.Configure(spec, []ossync.SourceImage{
		{
			Version:      testVersion,
			CleanVersion: testVersion,
			Capabilities: gardencorev1beta1.Capabilities{"architecture": {"amd64"}},
			Regions:      []ossync.RegionImage{{Region: region1, ID: "capability-uuid"}},
		},
	})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cfg := parseConfig(t, spec)
	v := findVersion(findImage(cfg, imageName), testVersion)
	if v == nil {
		t.Fatalf("version %s not found", testVersion)
	}
	if len(v.Regions) != 0 {
		t.Errorf("got %d legacy regions, want 0 (must be cleared on migration to capability flavors)", len(v.Regions))
	}
	if len(v.CapabilityFlavors) != 1 {
		t.Fatalf("got %d capabilityFlavors, want 1", len(v.CapabilityFlavors))
	}
	if id := v.CapabilityFlavors[0].Regions[0].ID; id != "capability-uuid" {
		t.Errorf("flavor region ID = %q, want capability-uuid", id)
	}
}
