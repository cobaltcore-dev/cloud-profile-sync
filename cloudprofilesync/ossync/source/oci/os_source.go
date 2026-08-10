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
	// chostFeature represent having containerd
	chostFeature = "chost"
	// pxeFeature represent pxe boot build
	pxeFeature     = "_pxe"
	sciFeature     = "sci"
	sciBaseFeature = "scibase"
	// capiFeature includes server, khost, and PXE; excludes SELinux and firewall
	capiFeature = "capi"
	// USIFeature shows UEFI build
	usiFeature    = "_usi"
	usiDevFeature = "_usidev"

	architectureCapability = "architecture"
	featureCapability      = "feature"
)

// validFeatureValues is the allowlist of feature values extracted from the feature_set annotation.
var validFeatureValues = map[string]struct{}{
	chostFeature:   {},
	pxeFeature:     {},
	sciFeature:     {},
	sciBaseFeature: {},
	capiFeature:    {},
	usiFeature:     {},
	usiDevFeature:  {},
}

func filterFeatureSet(featureSet string) []string {
	raw := strings.Split(featureSet, ",")
	seen := make(map[string]struct{}, len(raw))
	result := make([]string, 0, len(raw))
	for _, f := range raw {
		f = strings.TrimSpace(f)
		if _, valid := validFeatureValues[f]; !valid {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		result = append(result, f)
	}
	return result
}

type Result[T any] struct {
	value T
	err   error
}

type OCI struct {
	log  logr.Logger
	repo *remote.Repository
	sema *semaphore.Weighted
}

func NewOCI(params ocirepo.Params, parallel int64, log logr.Logger) (*OCI, error) {
	repo, err := ocirepo.New(params)
	if err != nil {
		return nil, err
	}

	return &OCI{
		log:  log,
		repo: repo,
		sema: semaphore.NewWeighted(parallel),
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
			arch, ok := manifest.Annotations["architecture"]
			if !ok {
				out <- Result[ossync.SourceImage]{err: fmt.Errorf("tag %s: architecture annotation not found", tag)}
				return
			}
			var capabilities gardencorev1beta1.Capabilities
			var cleanVersion string
			var supportInPlaceUpdate bool
			if featureSet, ok := manifest.Annotations["feature_set"]; ok {
				if version, ok := manifest.Annotations["version"]; ok {
					features := filterFeatureSet(featureSet)
					if len(features) > 0 {
						capabilities = gardencorev1beta1.Capabilities{
							architectureCapability: {arch},
							featureCapability:      features,
						}
						cleanVersion = version
						supportInPlaceUpdate = slices.Contains(features, usiFeature)
					}
				}
			}
			out <- Result[ossync.SourceImage]{
				value: ossync.SourceImage{
					Version:              strings.ReplaceAll(tag, "_", "+"), // Follow the helm convention
					CleanVersion:         cleanVersion,
					Architectures:        []string{arch},
					Capabilities:         capabilities,
					SupportInPlaceUpdate: supportInPlaceUpdate,
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
