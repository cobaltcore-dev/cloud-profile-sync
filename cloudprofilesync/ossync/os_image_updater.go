// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ossync

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Capability-related helpers (CapabilitiesEqual, mergeCapabilityFlavor,
// allowedCapabilityValues, filterCapabilities) and their constants live in
// capabilities.go.

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
	// SupportInPlaceUpdate indicates whether the image supports in-place OS updates.
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

func validateImageVersions(log logr.Logger, versions []SourceImage) []SourceImage {
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
	Log                 logr.Logger
	Source              Source
	Provider            Provider
	ImageName           string
	EnableCapabilities  bool
	MinVersionForUpdate *string
}

func resolveExpiration(fromSource, existing *metav1.Time) *metav1.Time {
	return cmp.Or(existing, fromSource)
}

func buildInPlaceUpdates(supported bool, minVersionForUpdate *string) *gardenerv1beta1.InPlaceUpdates {
	if !supported {
		return nil
	}
	return &gardenerv1beta1.InPlaceUpdates{
		Supported:           true,
		MinVersionForUpdate: minVersionForUpdate,
	}
}

func (iu *ImageUpdater) Update(ctx context.Context, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	sourceImages, err := iu.Source.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve image versions from OCI registry: %w", err)
	}
	sourceImages = validateImageVersions(iu.Log, sourceImages)
	if iu.MinVersionForUpdate == nil && slices.ContainsFunc(sourceImages, func(si SourceImage) bool {
		return si.SupportInPlaceUpdate
	}) {
		iu.Log.Info("minVersionForUpdate not set — in-place updates will not be available for existing worker pools", "image", iu.ImageName)
	}

	// Images from a source arrive in no guaranteed order. A changed order
	// in the source images may lead to a changed order in the CloudProfile,
	// causing unnecesscary reconciliations.
	slices.SortFunc(sourceImages, func(a, b SourceImage) int {
		if c := cmp.Compare(a.effectiveVersion(), b.effectiveVersion()); c != 0 {
			return c
		}
		return cmp.Compare(a.Version, b.Version)
	})

	allowed := allowedCapabilityValues(cpSpec.MachineCapabilities)
	for i := range sourceImages {
		sourceImages[i].Capabilities = filterCapabilities(sourceImages[i].Capabilities, allowed)
	}
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

	for _, src := range sourceImages {
		iu.upsertLegacyVersion(image, existingVersions, src)
		if iu.EnableCapabilities && src.CleanVersion != "" {
			iu.upsertCleanVersion(image, existingVersions, src)
		}
	}

	if iu.Provider != nil {
		if err := iu.Provider.Configure(cpSpec, sourceImages); err != nil {
			return fmt.Errorf("failed to invoke provider: %w", err)
		}
	}
	return nil
}

// upsertLegacyVersion writes or updates the full-tag version entry (legacy path, safe for running Shoots).
func (iu *ImageUpdater) upsertLegacyVersion(image *gardenerv1beta1.MachineImage, existingVersions map[string]int, src SourceImage) {
	if idx, exists := existingVersions[src.Version]; exists {
		v := &image.Versions[idx]
		v.Architectures = src.Architectures
		v.Classification = src.Classification                                      //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
		v.ExpirationDate = resolveExpiration(src.ExpirationDate, v.ExpirationDate) //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
		v.InPlaceUpdates = buildInPlaceUpdates(src.SupportInPlaceUpdate, iu.MinVersionForUpdate)
		return
	}
	// Moving this check to filterImages() would break the core architectural goal of GEP-33
	// as it intentionally decouples the OCI registry tag from the semantic OS version.
	// In the future, teams might push images with tags like build-0849f313 or 2026-06-release;
	// as long as CleanVersion is a valid SemVer (e.g., 2262.0.0), the extension routes to it.
	if _, parseErr := semver.Parse(src.Version); parseErr != nil {
		iu.Log.V(1).Info("skipping legacy entry in spec.machineImages because original tag is not valid semver", "version", src.Version)
		return
	}
	image.Versions = append(image.Versions, gardenerv1beta1.MachineImageVersion{
		ExpirableVersion: gardenerv1beta1.ExpirableVersion{
			Version:        src.Version,
			Classification: src.Classification, //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
			ExpirationDate: src.ExpirationDate, //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
		},
		Architectures:  src.Architectures,
		InPlaceUpdates: buildInPlaceUpdates(src.SupportInPlaceUpdate, iu.MinVersionForUpdate),
	})
	existingVersions[src.Version] = len(image.Versions) - 1
}

// upsertCleanVersion writes or updates the clean semantic version entry (GEP-33 dual-write).
// When CleanVersion == Version the entry was already registered by upsertLegacyVersion;
// the existing-entry branch merges the capability flavor without re-writing other fields.
func (iu *ImageUpdater) upsertCleanVersion(image *gardenerv1beta1.MachineImage, existingVersions map[string]int, src SourceImage) {
	if idx, exists := existingVersions[src.CleanVersion]; exists {
		existing := &image.Versions[idx]
		for _, arch := range src.Architectures {
			if !slices.Contains(existing.Architectures, arch) {
				existing.Architectures = append(existing.Architectures, arch)
			}
		}
		existing.Classification = src.Classification                                             //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
		existing.ExpirationDate = resolveExpiration(src.ExpirationDate, existing.ExpirationDate) //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
		existing.InPlaceUpdates = buildInPlaceUpdates(src.SupportInPlaceUpdate, iu.MinVersionForUpdate)
		existing.CapabilityFlavors = mergeCapabilityFlavor(existing.CapabilityFlavors, src.Capabilities)
		return
	}
	v := gardenerv1beta1.MachineImageVersion{
		ExpirableVersion: gardenerv1beta1.ExpirableVersion{
			Version:        src.CleanVersion,
			Classification: src.Classification, //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
			ExpirationDate: src.ExpirationDate, //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
		},
		Architectures:     slices.Clone(src.Architectures),
		CapabilityFlavors: mergeCapabilityFlavor(nil, src.Capabilities),
		InPlaceUpdates:    buildInPlaceUpdates(src.SupportInPlaceUpdate, iu.MinVersionForUpdate),
	}
	image.Versions = append(image.Versions, v)
	existingVersions[src.CleanVersion] = len(image.Versions) - 1
}
