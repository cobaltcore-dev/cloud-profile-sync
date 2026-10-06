// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package openstack

import (
	"cmp"
	"encoding/json"
	"slices"

	openstackv1alpha1 "github.com/gardener/gardener-extension-provider-openstack/pkg/apis/openstack/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

type OpenStackProvider struct {
	ImageName          string
	EnableCapabilities bool
}

func upsertRegion(regions []openstackv1alpha1.RegionIDMapping, name, id string) []openstackv1alpha1.RegionIDMapping {
	for i := range regions {
		if regions[i].Name == name {
			regions[i].ID = id
			return regions
		}
	}
	return append(regions, openstackv1alpha1.RegionIDMapping{Name: name, ID: id})
}

func sortRegions(regions []openstackv1alpha1.RegionIDMapping) {
	slices.SortFunc(regions, func(a, b openstackv1alpha1.RegionIDMapping) int {
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func (p *OpenStackProvider) Configure(pc *runtime.RawExtension, versions []ossync.SourceImage) (*runtime.RawExtension, error) {
	var cfg openstackv1alpha1.CloudProfileConfig
	if pc != nil {
		if err := json.Unmarshal(pc.Raw, &cfg); err != nil {
			return nil, err
		}
	}

	imageIndex := slices.IndexFunc(cfg.MachineImages, func(m openstackv1alpha1.MachineImages) bool {
		return m.Name == p.ImageName
	})
	if imageIndex == -1 {
		imageIndex = len(cfg.MachineImages)
		cfg.MachineImages = append(cfg.MachineImages, openstackv1alpha1.MachineImages{
			Name:     p.ImageName,
			Versions: []openstackv1alpha1.MachineImageVersion{},
		})
	}
	image := &cfg.MachineImages[imageIndex]

	existingVersions := make(map[string]int, len(image.Versions))
	for i, v := range image.Versions {
		existingVersions[v.Version] = i
	}

	for _, src := range versions {
		idx, exists := existingVersions[src.Version]
		if !exists {
			idx = len(image.Versions)
			image.Versions = append(image.Versions, openstackv1alpha1.MachineImageVersion{
				Version: src.Version,
			})
			existingVersions[src.Version] = idx
		}
		entry := &image.Versions[idx]

		if p.EnableCapabilities && src.Capabilities != nil {
			// validator.admission-openstack.extensions.gardener.cloud forbids both
			// regions and capabilityFlavors on the same version entry.
			entry.Regions = nil

			flavorIdx := slices.IndexFunc(entry.CapabilityFlavors, func(f openstackv1alpha1.MachineImageFlavor) bool {
				return ossync.CapabilitiesEqual(f.Capabilities, src.Capabilities)
			})
			if flavorIdx == -1 {
				flavorIdx = len(entry.CapabilityFlavors)
				entry.CapabilityFlavors = append(entry.CapabilityFlavors, openstackv1alpha1.MachineImageFlavor{
					Capabilities: src.Capabilities,
				})
			}
			flavor := &entry.CapabilityFlavors[flavorIdx]
			for _, r := range src.Regions {
				flavor.Regions = upsertRegion(flavor.Regions, r.Region, r.ID)
			}
			sortRegions(flavor.Regions)
		} else {
			for _, r := range src.Regions {
				entry.Regions = upsertRegion(entry.Regions, r.Region, r.ID)
			}
			sortRegions(entry.Regions)
		}
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{Raw: raw}, nil
}
