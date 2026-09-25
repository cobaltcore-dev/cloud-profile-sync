// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package openstack

import (
	"cmp"
	"encoding/json"
	"slices"

	openstackv1alpha1 "github.com/gardener/gardener-extension-provider-openstack/pkg/apis/openstack/v1alpha1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
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

func (p *OpenStackProvider) Configure(cpSpec *gardencorev1beta1.CloudProfileSpec, versions []ossync.SourceImage) error {
	var cfg openstackv1alpha1.CloudProfileConfig
	if cpSpec.ProviderConfig != nil {
		if err := json.Unmarshal(cpSpec.ProviderConfig.Raw, &cfg); err != nil {
			return err
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

		for _, r := range src.Regions {
			entry.Regions = upsertRegion(entry.Regions, r.Region, r.ID)
		}
		// Sort regions by name so the marshaled ProviderConfig is stable across
		// reconciles; the source does not guarantee a consistent region order,
		// which would otherwise churn the CloudProfile and cause a reconcile loop.
		slices.SortFunc(entry.Regions, func(a, b openstackv1alpha1.RegionIDMapping) int {
			if c := cmp.Compare(a.Name, b.Name); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})

		if p.EnableCapabilities && src.Capabilities != nil {
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
			// Sort once after all of this src's regions are upserted; only the
			// touched flavor's regions were modified.
			slices.SortFunc(flavor.Regions, func(a, b openstackv1alpha1.RegionIDMapping) int {
				if c := cmp.Compare(a.Name, b.Name); c != 0 {
					return c
				}
				return cmp.Compare(a.ID, b.ID)
			})
		}
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	cpSpec.ProviderConfig = &runtime.RawExtension{Raw: raw}
	return nil
}
