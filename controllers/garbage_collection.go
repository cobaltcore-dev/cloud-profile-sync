// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/gc"
)

type KeppelClient struct{}

func (k *KeppelClient) GetTags(ctx context.Context, registry, repository string) (map[string]time.Time, error) {
	return gc.FetchKeppelTags(ctx, registry, repository)
}

func (r *Reconciler) getRegistryProvider(registry string) (RegistryClient, error) {
	if registry == "" {
		return nil, errors.New("registry cannot be empty")
	}
	if strings.Contains(strings.ToLower(registry), "keppel") {
		return &KeppelClient{}, nil
	}
	return nil, errors.New("no registry provider found for registry")
}

// Filter holds the parameters for garbage-collection filtering. It is a pure,
// I/O-free value: given a slice of SourceImages it returns the subset that
// should remain, without mutating the input or performing any network calls.
type Filter struct {
	// Tags maps SourceImage.Version to the time the tag was pushed to the
	// registry. Keys must be in SourceImage.Version space (normalized).
	Tags map[string]time.Time
	// Protected is the set of versions (full tags) and clean versions that are
	// in use (e.g. referenced by a Shoot) and must never be collected. Keys must
	// be in SourceImage.Version / CleanVersion space (normalized).
	Protected map[string]struct{}
	// Cutoff is the age boundary: images pushed before Cutoff are candidates for
	// collection.
	Cutoff time.Time
}

// Filter returns the subset of images that should remain. It never mutates its
// input and preserves order.
func (f *Filter) Filter(images []ossync.SourceImage) []ossync.SourceImage {
	kept := make([]ossync.SourceImage, 0, len(images))
	for _, img := range images {
		if f.keep(img) {
			kept = append(kept, img)
		}
	}
	return kept
}

func (f *Filter) keep(img ossync.SourceImage) bool {
	// Referenced versions are always kept, by full tag or by clean version.
	if _, ok := f.Protected[img.Version]; ok {
		return true
	}
	if img.CleanVersion != "" {
		if _, ok := f.Protected[img.CleanVersion]; ok {
			return true
		}
	}
	// Unknown push time → keep (cannot judge age).
	pushedAt, ok := f.Tags[img.Version]
	if !ok {
		return true
	}
	// Keep unless strictly older than the cutoff.
	return !pushedAt.Before(f.Cutoff)
}

func (r *Reconciler) buildGCFilter(ctx context.Context, mcp *v1alpha1.ManagedCloudProfile, update v1alpha1.MachineImageUpdate) (ossync.VersionFilter, error) {
	if mcp.Spec.GarbageCollection == nil || !mcp.Spec.GarbageCollection.Enabled {
		return nil, nil
	}
	// Garbage collection only removes machine image versions, so honor the
	// machine image pause here too.
	if mcp.Spec.MachineImagesPaused {
		return nil, nil
	}
	// Garbage collection is only supported for OCI-sourced images.
	if update.Source.OCI == nil {
		return nil, nil
	}
	if mcp.Spec.GarbageCollection.MaxAge.Duration < 0 {
		return nil, fmt.Errorf("invalid garbage collection maxAge: %s", mcp.Spec.GarbageCollection.MaxAge.String())
	}

	registryClient, err := r.RegistryProviderFunc(update.Source.OCI.Registry)
	if err != nil {
		return nil, fmt.Errorf("no registry provider found for registry %q: %w", update.Source.OCI.Registry, err)
	}
	rawTags, err := registryClient.GetTags(ctx, update.Source.OCI.Registry, update.Source.OCI.Repository)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tags: %w", err)
	}
	// Normalize registry tags into SourceImage.Version space.
	tags := make(map[string]time.Time, len(rawTags))
	for tag, pushedAt := range rawTags {
		tags[gc.NormalizeTag(tag)] = pushedAt
	}

	protected, err := r.referencedVersions(ctx, mcp.Name, update.ImageName)
	if err != nil {
		return nil, fmt.Errorf("failed to determine referenced versions for garbage collection: %w", err)
	}

	return &Filter{
		Tags:      tags,
		Protected: protected,
		Cutoff:    time.Now().Add(-mcp.Spec.GarbageCollection.MaxAge.Duration),
	}, nil
}

func (r *Reconciler) referencedVersions(ctx context.Context, cloudProfileName, imageName string) (map[string]struct{}, error) {
	shootList := &gardenerv1beta1.ShootList{}
	if err := r.List(ctx, shootList, client.InNamespace(metav1.NamespaceAll)); err != nil {
		return nil, fmt.Errorf("failed to list Shoots: %w", err)
	}

	referenced := make(map[string]struct{})
	for _, shoot := range shootList.Items {
		if shoot.Spec.CloudProfile == nil || shoot.Spec.CloudProfile.Name != cloudProfileName {
			continue
		}
		for _, worker := range shoot.Spec.Provider.Workers {
			if worker.Machine.Image == nil || worker.Machine.Image.Name != imageName {
				continue
			}
			if worker.Machine.Image.Version != nil {
				referenced[gc.NormalizeTag(*worker.Machine.Image.Version)] = struct{}{}
			}
		}
	}
	return referenced, nil
}
