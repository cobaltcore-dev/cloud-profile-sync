// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

import (
	"context"
	"fmt"
	"slices"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
)

func filterImages(log logr.Logger, versions []SourceImage) []SourceImage {
	filtered := make([]SourceImage, 0, len(versions))
	for _, version := range versions {
		_, err := semver.Parse(version.Version)
		if err != nil {
			log.V(1).Info("skipping invalid version", "version", version.Version)
			continue
		}
		if len(version.Architectures) == 0 {
			log.V(1).Info("skipping version with no architectures", "version", version.Version)
			continue
		}
		filtered = append(filtered, version)
	}
	return filtered
}

type ImageUpdater struct {
	Log       logr.Logger
	Source    Source
	Provider  Provider
	ImageName string
}

func (iu *ImageUpdater) Update(ctx context.Context, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	sourceImages, err := iu.Source.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve images version from oci registry: %w", err)
	}
	iu.Log.Info("checked source", "image", iu.ImageName)
	sourceImages = filterImages(iu.Log, sourceImages)
	imageIndex := slices.IndexFunc(cpSpec.MachineImages, func(img gardenerv1beta1.MachineImage) bool {
		return img.Name == iu.ImageName
	})
	if imageIndex == -1 {
		cpSpec.MachineImages = append(cpSpec.MachineImages, gardenerv1beta1.MachineImage{Name: iu.ImageName})
		imageIndex = len(cpSpec.MachineImages) - 1
	}
	image := &cpSpec.MachineImages[imageIndex]
	existingVersions := make(map[string]int, len(image.Versions))
	for idx, version := range image.Versions {
		existingVersions[version.Version] = idx
	}
	for _, sourceImage := range sourceImages {
		if idx, exists := existingVersions[sourceImage.Version]; exists {
			image.Versions[idx].Architectures = sourceImage.Architectures
		} else {
			image.Versions = append(image.Versions, gardenerv1beta1.MachineImageVersion{
				ExpirableVersion: gardenerv1beta1.ExpirableVersion{
					Version: sourceImage.Version,
				},
				Architectures: sourceImage.Architectures,
			})
		}
	}
	if iu.Provider != nil {
		if err := iu.Provider.Configure(cpSpec, sourceImages); err != nil {
			return fmt.Errorf("failed to invoke provider: %w", err)
		}
	}
	return nil
}
