// Copyright 2025 SAP SE
// SPDX-License-Identifier: Apache-2.0

package cloudprofilesync

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/blang/semver/v4"
	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type NamedSource struct {
	Source
	Name string
}

type Runnable struct {
	Client   client.Client
	Log      logr.Logger
	Source   NamedSource
	Provider Provider
	Profile  types.NamespacedName
}

func (r *Runnable) Start(ctx context.Context) error {
	wait.JitterUntilWithContext(ctx, r.reconcile, time.Minute, 1.1, false)
	return nil
}

func (r *Runnable) reconcile(ctx context.Context) {
	if err := r.patchCloudProfile(ctx); err != nil {
		r.Log.Error(err, "failed to check source")
	}
}

func (r *Runnable) patchCloudProfile(ctx context.Context) error {
	var cloudProfile v1beta1.CloudProfile
	if err := r.Client.Get(ctx, r.Profile, &cloudProfile); err != nil {
		return fmt.Errorf("failed to get cloud profile: %w", err)
	}
	unmodified := cloudProfile.DeepCopy()
	if err := r.CheckSource(ctx, &cloudProfile); err != nil {
		return err
	}
	if err := r.Client.Patch(ctx, &cloudProfile, client.MergeFrom(unmodified)); err != nil {
		return fmt.Errorf("failed to patch cloud profile: %w", err)
	}
	r.Log.Info("updated cloud profile", "name", r.Source.Name)
	return nil
}

func (r *Runnable) getVersions(ctx context.Context) ([]SourceImage, error) {
	versions, err := r.Source.GetVersions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get versions from source: %w", err)
	}
	filtered := make([]SourceImage, 0, len(versions))
	for _, version := range versions {
		_, err := semver.Parse(version.Version)
		if err != nil {
			r.Log.Info("skipping invalid version", "version", version.Version)
			continue
		}
		if len(version.Architectures) == 0 {
			r.Log.Info("skipping version with no architectures", "version", version.Version)
			continue
		}
		filtered = append(filtered, version)
	}
	return filtered, nil
}

func (r *Runnable) CheckSource(ctx context.Context, cloudProfile *v1beta1.CloudProfile) error {
	versions, err := r.getVersions(ctx)
	if err != nil {
		return err
	}
	imageIndex := slices.IndexFunc(cloudProfile.Spec.MachineImages, func(mi v1beta1.MachineImage) bool {
		return mi.Name == r.Source.Name
	})
	if imageIndex == -1 {
		cloudProfile.Spec.MachineImages = append(cloudProfile.Spec.MachineImages, v1beta1.MachineImage{Name: r.Source.Name})
		imageIndex = len(cloudProfile.Spec.MachineImages) - 1
	}
	image := &cloudProfile.Spec.MachineImages[imageIndex]
	existingVersions := make(map[string]int, len(image.Versions))
	for idx, version := range image.Versions {
		existingVersions[version.Version] = idx
	}
	for _, version := range versions {
		if idx, exists := existingVersions[version.Version]; exists {
			image.Versions[idx].Architectures = version.Architectures
		} else {
			image.Versions = append(image.Versions, v1beta1.MachineImageVersion{
				ExpirableVersion: v1beta1.ExpirableVersion{
					Version: version.Version,
				},
				Architectures: version.Architectures,
			})
		}
	}
	if r.Provider != nil {
		if err := r.Provider.Configure(cloudProfile, versions); err != nil {
			return fmt.Errorf("failed to configure provider: %w", err)
		}
	}
	return nil
}
