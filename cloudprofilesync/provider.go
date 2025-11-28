// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

import (
	"encoding/json"
	"slices"

	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/ironcore-dev/gardener-extension-provider-ironcore-metal/pkg/apis/metal/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

type Provider interface {
	Configure(cloudProfile *v1beta1.CloudProfile, versions []SourceImage) error
}

type IroncoreProvider struct {
	Registry   string
	Repository string
	ImageName  string
}

func (p *IroncoreProvider) Configure(cloudProfile *v1beta1.CloudProfile, versions []SourceImage) error {
	var cfg v1alpha1.CloudProfileConfig
	if cloudProfile.Spec.ProviderConfig != nil {
		if err := json.Unmarshal(cloudProfile.Spec.ProviderConfig.Raw, &cfg); err != nil {
			return err
		}
	}
	imageIndex := slices.IndexFunc(cfg.MachineImages, func(m v1alpha1.MachineImages) bool {
		return m.Name == p.ImageName
	})
	if imageIndex == -1 {
		cfg.MachineImages = append(cfg.MachineImages, v1alpha1.MachineImages{
			Name:     p.ImageName,
			Versions: []v1alpha1.MachineImageVersion{},
		})
		imageIndex = len(cfg.MachineImages) - 1
	}
	image := &cfg.MachineImages[imageIndex]

	existingRefs := map[string]struct{}{}
	for _, version := range image.Versions {
		existingRefs[version.Image] = struct{}{}
	}

	for _, version := range versions {
		ref := p.Registry + "/" + p.Repository + ":" + version.Version
		if _, ok := existingRefs[ref]; ok {
			continue
		}
		for _, arch := range version.Architectures {
			image.Versions = append(image.Versions, v1alpha1.MachineImageVersion{
				Version:      version.Version,
				Image:        ref,
				Architecture: &arch,
			})
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	cloudProfile.Spec.ProviderConfig = &runtime.RawExtension{
		Raw: raw,
	}
	return err
}
