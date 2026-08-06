// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package kubernetessync

import (
	"context"
	"fmt"
	"time"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
)

// ExpirableVersion is a Kubernetes version with an optional classification and
// expiration date, as read from a GitHub versions file.
type ExpirableVersion struct {
	Version        string                                `yaml:"version"`
	Classification gardenerv1beta1.VersionClassification `yaml:"classification"`
	ExpirationDate *time.Time                            `yaml:"expirationDate"`
}

// KubernetesVersionSource is the single interface for sources that return
// Kubernetes versions ready to assign to a CloudProfile.
type KubernetesVersionSource interface {
	FetchVersions(ctx context.Context) ([]gardenerv1beta1.ExpirableVersion, error)
}

// KubernetesImageUpdater writes Kubernetes versions to a CloudProfileSpec,
// dropping any version whose expiration date has already passed the configured
// threshold.
type KubernetesImageUpdater struct {
	Source              KubernetesVersionSource
	ExpirationThreshold time.Duration
}

func NewKubernetesImageUpdater(source KubernetesVersionSource, expirationThreshold time.Duration) *KubernetesImageUpdater {
	return &KubernetesImageUpdater{
		Source:              source,
		ExpirationThreshold: expirationThreshold,
	}
}

func (ku *KubernetesImageUpdater) Update(ctx context.Context, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	versions, err := ku.Source.FetchVersions(ctx)
	if err != nil {
		return fmt.Errorf("fetching kubernetes versions: %w", err)
	}

	deleteThreshold := time.Now().Add(-ku.ExpirationThreshold)
	cpVersions := make([]gardenerv1beta1.ExpirableVersion, 0, len(versions))
	for _, v := range versions {
		if v.ExpirationDate != nil && v.ExpirationDate.Time.Before(deleteThreshold) { //nolint:staticcheck
			continue
		}
		cpVersions = append(cpVersions, v)
	}

	cpSpec.Kubernetes.Versions = cpVersions
	return nil
}
