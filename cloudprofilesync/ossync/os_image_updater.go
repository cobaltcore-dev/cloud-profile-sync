// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ossync

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SourceImage struct {
	// Version is the full tag from the registry (used as version key for legacy images).
	Version string
	// CleanVersion is the version from the "version" OCI annotation (e.g. "2262.0.0").
	// When set, flavors are grouped under it in the CloudProfile instead of the full tag.
	CleanVersion string
	// TODO: deprecate once all images carry capability annotations; use Capabilities["architecture"] instead.
	Architectures []string
	// Capabilities holds parsed OCI manifest annotations. Nil means the image
	// predates capability annotations and should use the legacy format.
	Capabilities gardenerv1beta1.Capabilities
	// SupportInPlaceUpdate hold value if image supports in place updates
	SupportInPlaceUpdate bool
	// Regions maps a region to the provider-specific image identifier (e.g. an
	// OpenStack Glance image UUID) for this version. It is nil for sources whose
	// images are not region-specific (e.g. OCI).
	Regions []RegionImage
	// Classification is the lifecycle state of the image version. Nil means unset (supported).
	Classification *gardenerv1beta1.VersionClassification
	// ExpirationDate is the date after which the version should no longer be used.
	ExpirationDate *metav1.Time
}

// RegionImage is the image identifier for a single version in a single region.
type RegionImage struct {
	// Region is the name of the region (e.g. "eu-de-1").
	Region string
	// ID is the image identifier in that region (e.g. a Glance image UUID).
	ID string
}

// effectiveVersion returns CleanVersion when available, falling back to Version.
func (s SourceImage) effectiveVersion() string {
	if s.CleanVersion != "" {
		return s.CleanVersion
	}
	return s.Version
}

type Source interface {
	GetVersions(ctx context.Context) ([]SourceImage, error)
}

type Provider interface {
	Configure(cloudProfile *gardenerv1beta1.CloudProfileSpec, versions []SourceImage) error
}

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
			} else {
				log.V(1).Info("ignoring invalid clean version annotation", "tag", version.Version, "cleanVersion", version.CleanVersion)
				version.CleanVersion = ""
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

// resolveExpiration decides the expiration date to write for a source image.
func (iu *ImageUpdater) resolveExpiration(src SourceImage, existing *metav1.Time) *metav1.Time {
	isDeprecated := src.Classification != nil && *src.Classification == gardenerv1beta1.ClassificationDeprecated
	if !isDeprecated {
		return src.ExpirationDate
	}
	if existing != nil {
		return existing
	}
	if src.ExpirationDate != nil {
		return src.ExpirationDate
	}
	now := metav1.NewTime(time.Now())
	return &now
}

// mergeCapabilityFlavor appends the flavor from src to existing if not already present.
func mergeCapabilityFlavor(existing []gardenerv1beta1.MachineImageFlavor, caps gardenerv1beta1.Capabilities) []gardenerv1beta1.MachineImageFlavor {
	if len(caps) == 0 {
		return existing
	}
	for _, f := range existing {
		if capabilitiesEqual(f.Capabilities, caps) {
			return existing
		}
	}
	return append(existing, gardenerv1beta1.MachineImageFlavor{Capabilities: caps})
}

func capabilitiesEqual(a, b gardenerv1beta1.Capabilities) bool {
	if len(a) != len(b) {
		return false
	}
	for k, aVals := range a {
		bVals, ok := b[k]
		if !ok {
			return false
		}
		if !slices.Equal(aVals, bVals) {
			return false
		}
	}
	return true
}

func inPlaceUpdates(supported bool) *gardenerv1beta1.InPlaceUpdates {
	if !supported {
		return nil
	}
	return &gardenerv1beta1.InPlaceUpdates{Supported: true}
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
			image.Versions[idx].Classification = sourceImage.Classification //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
			// Stamp expiration once on the transition to deprecated; preserve it thereafter.
			image.Versions[idx].ExpirationDate = iu.resolveExpiration(sourceImage, image.Versions[idx].ExpirationDate) //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
			image.Versions[idx].InPlaceUpdates = inPlaceUpdates(sourceImage.SupportInPlaceUpdate)
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
						Version:        sourceImage.Version,
						Classification: sourceImage.Classification,
						ExpirationDate: iu.resolveExpiration(sourceImage, nil),
					},
					Architectures: sourceImage.Architectures,
				})
				if sourceImage.SupportInPlaceUpdate {
					image.Versions[len(image.Versions)-1].InPlaceUpdates = &gardenerv1beta1.InPlaceUpdates{
						Supported: sourceImage.SupportInPlaceUpdate,
					}
				}
				existingVersions[sourceImage.Version] = len(image.Versions) - 1
			}
		}

		// When capabilities are enabled, also write/update the clean version entry.
		// When CleanVersion == Version the entry already exists from the legacy path above;
		// the existing-entry branch merges the flavor onto it without re-writing other fields.
		if iu.EnableCapabilities && sourceImage.CleanVersion != "" {
			if idx, exists := existingVersions[sourceImage.CleanVersion]; exists {
				existing := &image.Versions[idx]
				for _, arch := range sourceImage.Architectures {
					if !slices.Contains(existing.Architectures, arch) {
						existing.Architectures = append(existing.Architectures, arch)
					}
				}
				existing.Classification = sourceImage.Classification                                 //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
				existing.ExpirationDate = iu.resolveExpiration(sourceImage, existing.ExpirationDate) //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
				existing.InPlaceUpdates = inPlaceUpdates(sourceImage.SupportInPlaceUpdate)
				existing.CapabilityFlavors = mergeCapabilityFlavor(existing.CapabilityFlavors, sourceImage.Capabilities)
			} else {
				v := gardenerv1beta1.MachineImageVersion{
					ExpirableVersion: gardenerv1beta1.ExpirableVersion{
						Version:        sourceImage.CleanVersion,
						Classification: sourceImage.Classification,
						ExpirationDate: iu.resolveExpiration(sourceImage, nil),
					},
					Architectures:     slices.Clone(sourceImage.Architectures),
					CapabilityFlavors: mergeCapabilityFlavor(nil, sourceImage.Capabilities),
				}
				if sourceImage.SupportInPlaceUpdate {
					v.InPlaceUpdates = &gardenerv1beta1.InPlaceUpdates{Supported: true}
				}
				image.Versions = append(image.Versions, v)
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
