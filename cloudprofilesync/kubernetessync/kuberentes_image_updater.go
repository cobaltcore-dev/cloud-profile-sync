// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package kubernetessync

import (
	"context"
	"sort"
	"time"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExpirableVersion is a Kubernetes version with an optional classification and
// expiration date, as returned by a KubernetesImageProvider.
type ExpirableVersion struct {
	Version        string                                `yaml:"version"`
	Classification gardenerv1beta1.VersionClassification `yaml:"classification"`
	ExpirationDate *time.Time                            `yaml:"expirationDate"`
}

type KubernetesImageSource interface {
	FetchKubernetesVersion(ctx context.Context) ([]ExpirableVersion, error)
}

type KubernetesImageUpdater struct {
	Source              KubernetesImageSource
	ExpirationThreshold time.Duration
}

func NewKubernetesImageUpdater(source KubernetesImageSource, expirationThreshold time.Duration) *KubernetesImageUpdater {
	return &KubernetesImageUpdater{
		Source:              source,
		ExpirationThreshold: expirationThreshold,
	}
}

func (ku *KubernetesImageUpdater) Update(ctx context.Context, cpSpec *gardenerv1beta1.CloudProfileSpec) error {
	versions, err := ku.Source.FetchKubernetesVersion(ctx)
	if err != nil {
		return err
	}

	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Version < versions[j].Version
	})

	semver.MustParse(versions[0].Version)

	cpVersions := make([]gardenerv1beta1.ExpirableVersion, 0, len(versions))
	deleteThreshold := time.Now().Add(-ku.ExpirationThreshold)
	for _, v := range versions {
		if v.ExpirationDate != nil && v.ExpirationDate.Before(deleteThreshold) {
			continue
		}
		cpVersions = append(cpVersions, gardenerv1beta1.ExpirableVersion{
			Version:        v.Version,
			ExpirationDate: convertExpirationDate(v.ExpirationDate),
			Classification: &v.Classification,
		})
	}

	cpSpec.Kubernetes.Versions = cpVersions
	return nil
}

func convertExpirationDate(t *time.Time) *metav1.Time {
	if t == nil {
		return nil
	}

	return &metav1.Time{Time: *t}
}
