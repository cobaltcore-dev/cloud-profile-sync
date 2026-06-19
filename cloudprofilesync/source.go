// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

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
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

type Feature string

const (
	//ChostFeature represent having containerd
	ChostFeature = "chost"
	//PXEFeature represent pxe boot build
	PXEFeature     = "_pxe"
	SCIFeature     = "sci"
	SCIBaseFeature = "scibase"
	//CAPIFeature includes server, khost, and PXE; excludes SELinux and firewall
	CAPIFeature = "capi"
	// USIFeature shows UEFI build
	USIFeature    = "_usi"
	USIDevFeatrue = "_usidev"
)

// validFeatureValues is the allowlist of feature values extracted from the feature_set annotation.
var validFeatureValues = map[string]struct{}{
	ChostFeature:   {},
	PXEFeature:     {},
	SCIFeature:     {},
	SCIBaseFeature: {},
	CAPIFeature:    {},
	USIFeature:     {},
	USIDevFeatrue:  {},
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
	Capabilities gardencorev1beta1.Capabilities
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

type OCI struct {
	log  logr.Logger
	repo *remote.Repository
	sema *semaphore.Weighted
}

type OCIParams struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Username   string `json:"username"`
	Password   string `json:"password"` //nolint:gosec,nolintlint
	Parallel   int64  `json:"parallel"`
}

func NewOCI(params OCIParams, insecure bool, log logr.Logger) (*OCI, error) {
	// Create a new OCI repository
	repo, err := remote.NewRepository(params.Registry + "/" + params.Repository)
	if err != nil {
		return nil, err
	}

	if params.Username != "" && params.Password != "" {
		repo.Client = &auth.Client{
			Client: retry.DefaultClient,
			Cache:  auth.NewCache(),
			Credential: auth.StaticCredential(params.Registry, auth.Credential{
				Username: params.Username,
				Password: params.Password,
			}),
		}
	}
	repo.PlainHTTP = insecure

	return &OCI{
		log:  log,
		repo: repo,
		sema: semaphore.NewWeighted(params.Parallel),
	}, nil
}

func (o *OCI) GetVersions(ctx context.Context) ([]SourceImage, error) {
	tags := []string{}
	err := o.repo.Tags(ctx, "", func(t []string) error {
		tags = append(tags, t...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make(chan Result[SourceImage])
	for _, tag := range tags {
		go func() {
			if err := o.sema.Acquire(ctx, 1); err != nil {
				out <- Result[SourceImage]{err: err}
				return
			}
			defer o.sema.Release(1)
			_, reader, err := o.repo.FetchReference(ctx, tag)
			if err != nil {
				out <- Result[SourceImage]{err: fmt.Errorf("tag %s: failed to fetch manifest: %w", tag, err)}
				return
			}
			defer reader.Close()
			manifest := struct {
				Annotations map[string]string `json:"annotations"`
			}{}
			err = json.NewDecoder(reader).Decode(&manifest)
			if err != nil {
				out <- Result[SourceImage]{err: fmt.Errorf("tag %s: failed to decode manifest: %w", tag, err)}
				return
			}
			arch, ok := manifest.Annotations["architecture"]
			if !ok {
				out <- Result[SourceImage]{err: fmt.Errorf("tag %s: architecture annotation not found", tag)}
				return
			}
			var capabilities gardencorev1beta1.Capabilities
			var cleanVersion string
			if featureSet, ok := manifest.Annotations["feature_set"]; ok {
				if version, ok := manifest.Annotations["version"]; ok {
					features := filterFeatureSet(featureSet)
					if len(features) > 0 {
						capabilities = gardencorev1beta1.Capabilities{
							"architecture": {arch},
							"feature":      features,
						}
						cleanVersion = version
					}
				}
			}
			out <- Result[SourceImage]{
				value: SourceImage{
					Version:       strings.ReplaceAll(tag, "_", "+"), // Follow the helm convention
					CleanVersion:  cleanVersion,
					Architectures: []string{arch},
					Capabilities:  capabilities,
				},
			}
		}()
	}

	images := []SourceImage{}
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
