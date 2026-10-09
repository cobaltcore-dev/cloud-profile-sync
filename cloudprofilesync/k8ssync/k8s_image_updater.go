// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package k8ssync

import (
	"context"
	"errors"
	"fmt"
	"time"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
)

// KubernetesVersionSource is the single interface for sources that return
// Kubernetes versions ready to assign to a CloudProfile.
type KubernetesVersionSource interface {
	FetchVersions(ctx context.Context) ([]gardenerv1beta1.ExpirableVersion, error)
}

// KubernetesVersionUpdater writes Kubernetes versions to a CloudProfileSpec,
// dropping any version whose expiration date has already passed the configured
// threshold.
type KubernetesVersionUpdater struct {
	Source              KubernetesVersionSource
	ExpirationThreshold time.Duration
}

func NewKubernetesVersionUpdater(source KubernetesVersionSource, expirationThreshold time.Duration) *KubernetesVersionUpdater {
	return &KubernetesVersionUpdater{
		Source:              source,
		ExpirationThreshold: expirationThreshold,
	}
}

// Fetch performs the only network I/O: it retrieves the raw Kubernetes
// versions from the source. It touches no CloudProfileSpec, so it is safe to
// call once outside a CreateOrPatch mutate closure.
func (ku *KubernetesVersionUpdater) Fetch(ctx context.Context) ([]gardenerv1beta1.ExpirableVersion, error) {
	versions, err := ku.Source.FetchVersions(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching kubernetes versions: %w", err)
	}
	return versions, nil
}

// Apply filters the pre-fetched versions, dropping any whose expiration date has
// already passed the configured threshold, and returns the result. It performs
// no I/O. It refuses to return an empty list (returns an error instead) so a
// transient empty source never wipes the CloudProfile.
func (ku *KubernetesVersionUpdater) Apply(versions []gardenerv1beta1.ExpirableVersion) ([]gardenerv1beta1.ExpirableVersion, error) {
	cutoff := time.Now().Add(-ku.ExpirationThreshold)
	filteredVersions := make([]gardenerv1beta1.ExpirableVersion, 0, len(versions))
	for _, v := range versions {
		if v.ExpirationDate != nil && v.ExpirationDate.Time.Before(cutoff) { //nolint:staticcheck
			continue
		}
		filteredVersions = append(filteredVersions, v)
	}

	if len(filteredVersions) == 0 {
		return nil, errors.New("source returned no kubernetes versions after expiration filtering, refusing to wipe CloudProfile")
	}
	return filteredVersions, nil
}
