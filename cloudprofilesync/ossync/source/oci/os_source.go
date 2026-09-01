// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	"golang.org/x/sync/semaphore"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ocirepo"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

const (
	// usiCapabilityValue is the normalized capability value for the gardenlinux USI
	// (UEFI Secure Image) feature, which indicates support for in-place node updates.
	usiCapabilityValue = "usi"
	// featureSetAnnotation is the gardenlinux OCI annotation key that lists the
	// feature set of an image as a comma-separated list (e.g. "sci,_usi,_pxe").
	featureSetAnnotation = "feature_set"
)

// supportsInPlaceUpdate reports whether the gardenlinux image described by the
// given OCI annotations supports in-place node updates. It reads the feature_set
// annotation directly, independent of which capabilityKeys the OCI source is
// configured to expose, so USI detection is never accidentally suppressed.
func supportsInPlaceUpdate(annotations map[string]string) bool {
	raw, ok := annotations[featureSetAnnotation]
	if !ok {
		return false
	}
	return slices.Contains(filterAnnotationValues(raw), usiCapabilityValue)
}

// normalizeCapabilityValue strips leading underscores from a feature annotation value
// so it satisfies Gardener's requirement that capability values start with an
// alphanumeric character. Gardenlinux uses a leading '_' convention for UEFI variants
// (e.g. _usi, _pxe) that has no meaning in the Gardener capability key space.
func normalizeCapabilityValue(v string) string {
	return strings.TrimLeft(v, "_")
}

func filterAnnotationValues(raw string) []string {
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, f := range parts {
		f = strings.TrimSpace(f)
		capVal := normalizeCapabilityValue(f)
		if capVal == "" {
			continue
		}
		if _, dup := seen[capVal]; dup {
			continue
		}
		seen[capVal] = struct{}{}
		result = append(result, capVal)
	}
	return result
}

type Result[T any] struct {
	value T
	err   error
}

type OCI struct {
	log            logr.Logger
	repo           *remote.Repository
	sema           *semaphore.Weighted
	capabilityKeys []string
}

func NewOCI(params ocirepo.Params, parallel int64, log logr.Logger, capabilityKeys []string) (*OCI, error) {
	repo, err := ocirepo.New(params)
	if err != nil {
		return nil, err
	}

	return &OCI{
		log:            log,
		repo:           repo,
		sema:           semaphore.NewWeighted(parallel),
		capabilityKeys: capabilityKeys,
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

	out := make(chan Result[ossync.SourceImage])
	for _, tag := range tags {
		go func() {
			if err := o.sema.Acquire(ctx, 1); err != nil {
				out <- Result[ossync.SourceImage]{err: err}
				return
			}
			defer o.sema.Release(1)
			_, reader, err := o.repo.FetchReference(ctx, tag)
			if err != nil {
				out <- Result[ossync.SourceImage]{err: fmt.Errorf("tag %s: failed to fetch manifest: %w", tag, err)}
				return
			}
			defer reader.Close()
			manifest := struct {
				Annotations map[string]string `json:"annotations"`
			}{}
			err = json.NewDecoder(reader).Decode(&manifest)
			if err != nil {
				out <- Result[ossync.SourceImage]{err: fmt.Errorf("tag %s: failed to decode manifest: %w", tag, err)}
				return
			}
			arch, ok := manifest.Annotations[ossync.ArchitectureCapability]
			if !ok {
				out <- Result[ossync.SourceImage]{err: fmt.Errorf("tag %s: architecture annotation not found", tag)}
				return
			}
			cleanVersion, _ := manifest.Annotations["version"]
			var capabilities gardencorev1beta1.Capabilities
			if len(o.capabilityKeys) > 0 && cleanVersion != "" {
				caps := make(gardencorev1beta1.Capabilities, 1+len(o.capabilityKeys))
				caps[ossync.ArchitectureCapability] = []string{arch}
				for _, key := range o.capabilityKeys {
					raw, ok := manifest.Annotations[key]
					if !ok {
						continue
					}
					values := filterAnnotationValues(raw)
					if len(values) > 0 {
						caps[key] = values
					}
				}
				if len(caps) > 1 { // more than just architecture
					capabilities = caps
				}
			}
			out <- Result[ossync.SourceImage]{
				value: ossync.SourceImage{
					Version:              strings.ReplaceAll(tag, "_", "+"), // Follow the helm convention
					CleanVersion:         cleanVersion,
					Architectures:        []string{arch},
					Capabilities:         capabilities,
					SupportInPlaceUpdate: supportsInPlaceUpdate(manifest.Annotations),
				},
			}
		}()
	}

	images := []ossync.SourceImage{}
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
		images = append(images, result.value)
	}
	if len(skipped) > 0 {
		o.log.V(1).Info("skipped tags with errors", "count", len(skipped), "errors", errors.Join(skipped...))
	}
	if len(errs) == 0 && len(images) == 0 && len(tags) > 0 {
		return nil, fmt.Errorf("all %d tags were skipped; possible registry issue", len(tags))
	}
	return images, errors.Join(errs...)
}
