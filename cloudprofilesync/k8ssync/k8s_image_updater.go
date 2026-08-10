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

func (ku *KubernetesVersionUpdater) Update(ctx context.Context, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	versions, err := ku.Source.FetchVersions(ctx)
	if err != nil {
		return fmt.Errorf("fetching kubernetes versions: %w", err)
	}

	cutoff := time.Now().Add(-ku.ExpirationThreshold)
	filteredVersions := make([]gardenerv1beta1.ExpirableVersion, 0, len(versions))
	for _, v := range versions {
		if v.ExpirationDate != nil && v.ExpirationDate.Time.Before(cutoff) { //nolint:staticcheck
			continue
		}
		filteredVersions = append(filteredVersions, v)
	}

	if len(filteredVersions) == 0 {
		return errors.New("source returned no kubernetes versions after expiration filtering, refusing to wipe CloudProfile")
	}
	cpSpec.Kubernetes.Versions = filteredVersions
	return nil
}
