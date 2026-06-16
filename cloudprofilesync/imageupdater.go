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
	for _, version := range versions {
		if len(version.Architectures) == 0 {
			log.V(1).Info("skipping version with no architectures", "version", version.Version)
			continue
		}

		validLegacyTag := false
		if _, err := semver.Parse(version.Version); err == nil {
			validLegacyTag = true
		}

		validCleanVersion := false
		if version.CleanVersion != "" {
			// Found that we can have "1921.0" in annotations. It will be transformed to "1921.0.0"
			if parsed, err := semver.ParseTolerant(version.CleanVersion); err == nil {
				validCleanVersion = true
				version.CleanVersion = parsed.String()
			}
		}

		if !validLegacyTag && !validCleanVersion {
			log.V(1).Info("skipping invalid version (both tag and clean version are bad)", "tag", version.Version)
			continue
		}

		filtered = append(filtered, version)
	}
	return filtered
}

type ImageUpdater struct {
	Log                logr.Logger
	Source             Source
	Provider           Provider
	ImageName          string
	EnableCapabilities bool
}

func (iu *ImageUpdater) Update(ctx context.Context, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	sourceImages, err := iu.Source.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve image versions from OCI registry: %w", err)
	}
	sourceImages = filterImages(iu.Log, sourceImages)
	// Images from a source arrive in no guaranteed order. A changed order
	// in the source images may lead to a changed order in the CloudProfile,
	// causing unnecesscary reconciliations.
	slices.SortFunc(sourceImages, func(a, b SourceImage) int {
		if c := cmp.Compare(a.effectiveVersion(), b.effectiveVersion()); c != 0 {
			return c
		}
		return cmp.Compare(a.Version, b.Version)
	})
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
		// Always write the full tag version (legacy path, safe for running Shoots).
		if idx, exists := existingVersions[sourceImage.Version]; exists {
			image.Versions[idx].Architectures = sourceImage.Architectures
		} else {
			// Moving this check to filterImages() would break the core architectural goal of GEP-33
			// as it intentionally decouples the OCI registry tag from the semantic OS version
			// In the future, teams might push images with tags like build-0849f313 or 2026-06-release
			// As long as the CleanVersion annotation is a valid SemVer (e.g., 2262.0.0), the extension needs to route to it
			if _, err = semver.Parse(sourceImage.Version); err != nil {
				iu.Log.V(1).Info("skipping legacy entry in spec.machineImages because original tag is not valid semver", "version", sourceImage.Version)
			} else {
				image.Versions = append(image.Versions, gardenerv1beta1.MachineImageVersion{
					ExpirableVersion: gardenerv1beta1.ExpirableVersion{
						Version: sourceImage.Version,
					},
					Architectures: sourceImage.Architectures,
				})
				existingVersions[sourceImage.Version] = len(image.Versions) - 1
			}
		}

		// When capabilities are enabled, also write the clean version entry.
		if iu.EnableCapabilities && sourceImage.CleanVersion != "" && sourceImage.CleanVersion != sourceImage.Version {
			if idx, exists := existingVersions[sourceImage.CleanVersion]; exists {
				existing := &image.Versions[idx]
				for _, arch := range sourceImage.Architectures {
					if !slices.Contains(existing.Architectures, arch) {
						existing.Architectures = append(existing.Architectures, arch)
					}
				}
			} else {
				image.Versions = append(image.Versions, gardenerv1beta1.MachineImageVersion{
					ExpirableVersion: gardenerv1beta1.ExpirableVersion{
						Version: sourceImage.CleanVersion,
					},
					Architectures: slices.Clone(sourceImage.Architectures),
				})
				existingVersions[sourceImage.CleanVersion] = len(image.Versions) - 1
			}
		}
	}

	if iu.Provider != nil {
		if err := iu.Provider.Configure(cpSpec, sourceImages); err != nil {
			return fmt.Errorf("failed to invoke provider: %w", err)
		}
	}
	return nil
}
