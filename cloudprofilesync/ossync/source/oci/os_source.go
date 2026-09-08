// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	"golang.org/x/sync/semaphore"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ocirepo"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

const (
	// usiImageFeature is the value for the gardenlinux USI (UEFI Secure Image) feature,
	// which indicates support for in-place node updates.
	usiImageFeature = "_usi"
)

// supportsInPlaceUpdate reports whether the gardenlinux image described by the
// given OCI annotations supports in-place node updates. It reads the feature_set
// annotation directly, independent of the featureToCapabilityMap configuration,
// so USI detection is never accidentally suppressed.
func supportsInPlaceUpdate(annotations map[string]string) bool {
	raw, ok := annotations[ossync.FeatureSetAnnotation]
	if !ok {
		return false
	}
	tokens := splitAnnotationRaw(raw)
	_, usi := tokens[usiImageFeature]
	return usi
}

// splitAnnotationRaw splits a comma-separated annotation value into a set,
// preserving the original tokens without normalization (e.g. "_usidev" stays "_usidev").
// Used for exact FeatureSetValue matching.
func splitAnnotationRaw(raw string) map[string]struct{} {
	parts := strings.Split(raw, ",")
	out := make(map[string]struct{}, len(parts))
	for _, f := range parts {
		f = strings.TrimSpace(f)
		if f != "" {
			out[f] = struct{}{}
		}
	}
	return out
}

type collectedImage struct {
	image         ossync.SourceImage
	featureSetRaw string
}

type Result[T any] struct {
	value T
	err   error
}

type OCI struct {
	log                    logr.Logger
	repo                   *remote.Repository
	sema                   *semaphore.Weighted
	featureToCapabilityMap map[string]string
	imageFilter            *v1alpha1.ImageFilter
}

func NewOCI(params ocirepo.Params, parallel int64, log logr.Logger, featureToCapabilityMap map[string]string, imageFilter *v1alpha1.ImageFilter) (*OCI, error) {
	repo, err := ocirepo.New(params)
	if err != nil {
		return nil, err
	}
	return &OCI{
		log:                    log,
		repo:                   repo,
		sema:                   semaphore.NewWeighted(parallel),
		featureToCapabilityMap: featureToCapabilityMap,
		imageFilter:            imageFilter,
	}, nil
}

func (o *OCI) GetVersions(ctx context.Context) ([]ossync.SourceImage, error) {
	tags := []string{}
	err := o.repo.Tags(ctx, "", func(t []string) error {
		tags = append(tags, t...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make(chan Result[collectedImage])
	for _, tag := range tags {
		go func() {
			if err := o.sema.Acquire(ctx, 1); err != nil {
				out <- Result[collectedImage]{err: err}
				return
			}
			defer o.sema.Release(1)
			_, reader, err := o.repo.FetchReference(ctx, tag)
			if err != nil {
				out <- Result[collectedImage]{err: fmt.Errorf("tag %s: failed to fetch manifest: %w", tag, err)}
				return
			}
			defer reader.Close()
			manifest := struct {
				Annotations map[string]string `json:"annotations"`
			}{}
			err = json.NewDecoder(reader).Decode(&manifest)
			if err != nil {
				out <- Result[collectedImage]{err: fmt.Errorf("tag %s: failed to decode manifest: %w", tag, err)}
				return
			}
			arch, ok := manifest.Annotations[ossync.ArchitectureCapability]
			if !ok {
				out <- Result[collectedImage]{err: fmt.Errorf("tag %s: architecture annotation not found", tag)}
				return
			}
			cleanVersion, _ := manifest.Annotations["version"]
			rawAnnotation := manifest.Annotations[ossync.FeatureSetAnnotation]
			rawFeatureSet := splitAnnotationRaw(rawAnnotation)
			var capabilities gardencorev1beta1.Capabilities
			if len(o.featureToCapabilityMap) > 0 && cleanVersion != "" {
				caps := make(gardencorev1beta1.Capabilities, 1+len(o.featureToCapabilityMap))
				caps[ossync.ArchitectureCapability] = []string{arch}
				for featureSetValue, capabilityName := range o.featureToCapabilityMap {
					_, present := rawFeatureSet[featureSetValue]
					if present {
						caps[capabilityName] = []string{"true"}
					} else {
						caps[capabilityName] = []string{"false"}
					}
				}
				capabilities = caps
			}
			out <- Result[collectedImage]{
				value: collectedImage{
					image: ossync.SourceImage{
						Version:              strings.ReplaceAll(tag, "_", "+"), // Follow the helm convention
						CleanVersion:         cleanVersion,
						Architectures:        []string{arch},
						Capabilities:         capabilities,
						SupportInPlaceUpdate: supportsInPlaceUpdate(manifest.Annotations),
					},
					featureSetRaw: rawAnnotation,
				},
			}
		}()
	}

	var items []collectedImage
	var skipped []error
	var errs []error
	for range tags {
		result := <-out
		if result.err != nil {
			if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
				errs = append(errs, result.err)
			} else {
				skipped = append(skipped, result.err)
			}
			continue
		}
		items = append(items, result.value)
	}
	if len(skipped) > 0 {
		o.log.V(1).Info("skipped tags with errors", "count", len(skipped), "errors", errors.Join(skipped...))
	}
	images := applyImageFilter(o.log, items, o.imageFilter)
	if len(errs) == 0 && len(images) == 0 && len(skipped) == len(tags) {
		return nil, fmt.Errorf("all %d tags were skipped; possible registry issue", len(tags))
	}
	return images, errors.Join(errs...)
}

// applyImageFilter removes images whose feature_set annotations do not satisfy
// every required value in filter. No-op when filter is nil.
func applyImageFilter(log logr.Logger, items []collectedImage, filter *v1alpha1.ImageFilter) []ossync.SourceImage {
	images := make([]ossync.SourceImage, 0, len(items))
	for _, item := range items {
		if filter != nil && !passesImageFilter(splitAnnotationRaw(item.featureSetRaw), filter) {
			log.V(1).Info("image excluded by imageFilter", "version", item.image.Version)
			continue
		}
		images = append(images, item.image)
	}
	return images
}

// passesImageFilter reports whether the raw feature_set token set satisfies every
// required value in filter. Always returns true when filter is nil.
func passesImageFilter(rawFeatureSet map[string]struct{}, filter *v1alpha1.ImageFilter) bool {
	if filter == nil {
		return true
	}
	for _, req := range filter.RequiredFeatureSetValues {
		if _, ok := rawFeatureSet[req]; !ok {
			return false
		}
	}
	return true
}
