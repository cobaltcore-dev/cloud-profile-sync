// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
)

func filterImages(log logr.Logger, versions []SourceImage) []SourceImage {
	filtered := make([]SourceImage, 0, len(versions))
	log.Info("starting to filter source images", "total_versions", len(versions))
	for _, version := range versions {
		parsed, err := semver.Parse(version.Version)
		if err != nil {
			log.V(1).Info("skipping invalid version", "version", version.Version)
			continue
		}
		if len(version.Architectures) == 0 {
			log.V(1).Info("skipping version with no architectures", "version", version.Version)
			continue
		}
		log.V(1).Info("valid version found", "version", version.Version, "major", parsed.Major, "minor", parsed.Minor, "patch", parsed.Patch, "pre_release", parsed.Pre)
		filtered = append(filtered, version)
	}
	log.Info("finished filtering source images", "valid_versions", len(filtered))
	return filtered
}

type ImageUpdater struct {
	Log       logr.Logger
	Source    Source
	Provider  Provider
	ImageName string
}

func (iu *ImageUpdater) Update(ctx context.Context, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	iu.Log.Info("checking source", "image", iu.ImageName)
	sourceImages, err := iu.Source.GetVersions(ctx)
	if err != nil {
		iu.Log.Error(err, "failed to retrieve image versions from OCI registry", "image", iu.ImageName, "error_msg", err.Error())
		return fmt.Errorf("failed to retrieve image versions from OCI registry: %w", err)
	}
	iu.Log.Info("retrieved source images", "count", len(sourceImages), "image", iu.ImageName)
	sourceImages = filterImages(iu.Log, sourceImages)
	// Images from a source arrive in no guaranteed order. A changed order
	// in the source images may lead to a changed order in the CloudProfile,
	// causing unnecesscary reconciliations.
	iu.Log.Info("filtered valid images", "count", len(sourceImages), "image", iu.ImageName)
	for _, img := range sourceImages {
		iu.Log.V(1).Info("valid source image", "version", img.Version, "architectures", img.Architectures)
	}
	slices.SortFunc(sourceImages, func(a, b SourceImage) int {
		return cmp.Compare(a.Version, b.Version)
	})
	iu.Log.V(1).Info("sorted source images", "image", iu.ImageName)
	imageIndex := slices.IndexFunc(cpSpec.MachineImages, func(img gardenerv1beta1.MachineImage) bool {
		return img.Name == iu.ImageName
	})
	if imageIndex == -1 {
		cpSpec.MachineImages = append(cpSpec.MachineImages, gardenerv1beta1.MachineImage{Name: iu.ImageName})
		imageIndex = len(cpSpec.MachineImages) - 1
		iu.Log.Info("created new MachineImage entry", "image", iu.ImageName)
	}
	image := &cpSpec.MachineImages[imageIndex]
	existingVersions := make(map[string]int, len(image.Versions))
	for idx, version := range image.Versions {
		existingVersions[version.Version] = idx
	}
	iu.Log.V(1).Info("existing versions in CloudProfile", "count", len(existingVersions), "image", iu.ImageName)
	for _, sourceImage := range sourceImages {
		if idx, exists := existingVersions[sourceImage.Version]; exists {
			iu.Log.V(1).Info("updating existing version architectures", "version", sourceImage.Version, "architectures", sourceImage.Architectures)
			image.Versions[idx].Architectures = sourceImage.Architectures
		} else {
			iu.Log.Info("adding new version to CloudProfile", "version", sourceImage.Version, "architectures", sourceImage.Architectures)
			image.Versions = append(image.Versions, gardenerv1beta1.MachineImageVersion{
				ExpirableVersion: gardenerv1beta1.ExpirableVersion{
					Version: sourceImage.Version,
				},
				Architectures: sourceImage.Architectures,
			})
		}
	}
	if iu.Provider != nil {
		iu.Log.Info("invoking provider Configure", "image", iu.ImageName, "versions_count", len(sourceImages))
		if err := iu.Provider.Configure(cpSpec, sourceImages); err != nil {
			iu.Log.Error(err, "provider Configure failed", "image", iu.ImageName, "error_msg", err.Error())
			return fmt.Errorf("failed to invoke provider: %w", err)
		}
		iu.Log.Info("provider Configure succeeded", "image", iu.ImageName)
	}
	iu.Log.Info("finished updating CloudProfile for image", "image", iu.ImageName, "total_versions", len(image.Versions))
	return nil
}
