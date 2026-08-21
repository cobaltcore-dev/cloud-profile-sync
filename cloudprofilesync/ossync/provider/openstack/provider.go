package openstack

// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

import (
	"encoding/json"
	"slices"

	openstackv1alpha1 "github.com/gardener/gardener-extension-provider-openstack/pkg/apis/openstack/v1alpha1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

type OpenStackProvider struct {
	ImageName string
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
			existing := slices.IndexFunc(entry.Regions, func(m openstackv1alpha1.RegionIDMapping) bool {
				return m.Name == r.Region
			})
			if existing == -1 {
				entry.Regions = append(entry.Regions, openstackv1alpha1.RegionIDMapping{
					Name: r.Region,
					ID:   r.ID,
				})
				continue
			}
			// Update in place: a rebuilt image keeps the version but gets a new UUID.
			entry.Regions[existing].ID = r.ID
		}
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	cpSpec.ProviderConfig = &runtime.RawExtension{Raw: raw}
	return nil
}
