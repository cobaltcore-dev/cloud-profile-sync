// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

import (
	"encoding/json"
	"slices"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/ironcore-dev/gardener-extension-provider-ironcore-metal/pkg/apis/metal/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

type Provider interface {
	Configure(cloudProfile *gardencorev1beta1.CloudProfileSpec, versions []SourceImage) error
}

type IroncoreProvider struct {
	Registry           string
	Repository         string
	ImageName          string
	EnableCapabilities bool
}

func (p *IroncoreProvider) Configure(cpSpec *gardencorev1beta1.CloudProfileSpec, versions []SourceImage) error {
	var cfg v1alpha1.CloudProfileConfig
	if cpSpec.ProviderConfig != nil {
		if err := json.Unmarshal(cpSpec.ProviderConfig.Raw, &cfg); err != nil {
			return err
		}
	}
	imageIndex := slices.IndexFunc(cfg.MachineImages, func(m v1alpha1.MachineImages) bool {
		return m.Name == p.ImageName
	})
	if imageIndex == -1 {
		imageIndex = len(cfg.MachineImages)
		cfg.MachineImages = append(cfg.MachineImages, v1alpha1.MachineImages{
			Name:     p.ImageName,
			Versions: []v1alpha1.MachineImageVersion{},
		})
	}
	image := &cfg.MachineImages[imageIndex]

	existingVersions := make(map[string]int, len(image.Versions))
	for i, v := range image.Versions {
		existingVersions[v.Version] = i
	}

	for _, src := range versions {
		ref := p.Registry + "/" + p.Repository + ":" + src.Version

		// Always write the legacy flat entry keyed by the full tag.
		for _, arch := range src.Architectures {
			archCopy := arch
			alreadyPresent := slices.ContainsFunc(image.Versions, func(v v1alpha1.MachineImageVersion) bool {
				return v.Image == ref && v.Architecture != nil && *v.Architecture == arch
			})
			if !alreadyPresent {
				image.Versions = append(image.Versions, v1alpha1.MachineImageVersion{
					Version:      src.Version,
					Image:        ref,
					Architecture: &archCopy,
				})
			}
		}

		// When capabilities are enabled and the image carries capability metadata,
		// also write a CapabilityFlavors entry grouped under the clean version.
		if p.EnableCapabilities && src.Capabilities != nil && src.CleanVersion != "" && src.CleanVersion != src.Version {
			flavor := v1alpha1.MachineImageFlavor{
				Image:        ref,
				Capabilities: src.Capabilities,
			}
			if idx, exists := existingVersions[src.CleanVersion]; exists {
				existing := &image.Versions[idx]
				alreadyPresent := slices.ContainsFunc(existing.CapabilityFlavors, func(f v1alpha1.MachineImageFlavor) bool {
					return f.Image == ref
				})
				if !alreadyPresent {
					existing.CapabilityFlavors = append(existing.CapabilityFlavors, flavor)
				}
			} else {
				idx := len(image.Versions)
				image.Versions = append(image.Versions, v1alpha1.MachineImageVersion{
					Version:           src.CleanVersion,
					CapabilityFlavors: []v1alpha1.MachineImageFlavor{flavor},
				})
				existingVersions[src.CleanVersion] = idx
			}
		}
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	cpSpec.ProviderConfig = &runtime.RawExtension{Raw: raw}
	return nil
}
