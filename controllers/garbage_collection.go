// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package controllers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
	return fetchKeppelTags(ctx, registry, repository)
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

type KeppelTag struct {
	Name     string `json:"name"`
	PushedAt int64  `json:"pushed_at"`
}

type KeppelManifest struct {
	Digest   string      `json:"digest"`
	PushedAt int64       `json:"pushed_at"`
	Tags     []KeppelTag `json:"tags"`
}

type KeppelManifestsResponse struct {
	Manifests []KeppelManifest `json:"manifests"`
}

// normalizeTag applies the same tag normalization the OCI source does when
// building SourceImage.Version (helm convention: "_" -> "+"), so registry tags
// and Shoot-referenced versions are compared in SourceImage.Version space.
func normalizeTag(tag string) string {
	return strings.ReplaceAll(tag, "_", "+")
}

// buildGCFilter constructs the garbage-collection filter for a single machine
// image update, or returns a nil filter when garbage collection does not apply
// (GC disabled, machine images paused, non-OCI source). It performs all the
// cluster/registry I/O here — listing Shoots and querying the registry for push
// timestamps — and hands the result to the pure ossync/gc filter, which drops
// stale, unreferenced source images.
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
		tags[normalizeTag(tag)] = pushedAt
	}

	protected, err := r.referencedVersions(ctx, mcp.Name, update.ImageName)
	if err != nil {
		return nil, fmt.Errorf("failed to determine referenced versions for garbage collection: %w", err)
	}

	return &gc.Filter{
		Tags:      tags,
		Protected: protected,
		Cutoff:    time.Now().Add(-mcp.Spec.GarbageCollection.MaxAge.Duration),
	}, nil
}

// referencedVersions returns the set of image versions (full tags and/or clean
// versions) that are in use by any Shoot worker pool targeting the given
// CloudProfile and image. These must never be garbage-collected. The gc.Filter
// expands clean-version protection to the backing tags itself (it keeps any
// SourceImage whose CleanVersion is protected), so no provider config parsing
// is needed here.
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
				referenced[normalizeTag(*worker.Machine.Image.Version)] = struct{}{}
			}
		}
	}
	return referenced, nil
}

func fetchKeppelTags(ctx context.Context, registry, repository string) (map[string]time.Time, error) {
	baseURL := registryBaseURL(registry, false)

	keppelURL, err := keppelURL(baseURL, repository)
	if err != nil {
		return nil, fmt.Errorf("failed to build keppel URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, keppelURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create keppel request: %w", err)
	}

	tr := &http.Transport{
		TLSHandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: tr,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("keppel API returned status %d", resp.StatusCode)
		return nil, err
	}

	var result KeppelManifestsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	tagMap := make(map[string]time.Time)

	for _, m := range result.Manifests {
		for _, t := range m.Tags {
			if t.PushedAt == 0 {
				continue
			}
			tagMap[t.Name] = time.Unix(t.PushedAt, 0)
		}
	}

	return tagMap, nil
}

func keppelURL(baseURL, repository string) (string, error) {
	account, repo, err := splitKeppelRepository(repository)
	if err != nil {
		return "", err
	}

	keppelURL := fmt.Sprintf(
		"%s/keppel/v1/accounts/%s/repositories/%s/_manifests",
		baseURL,
		account,
		repo,
	)

	return keppelURL, nil
}

func registryBaseURL(registryHost string, insecure bool) string {
	scheme := "https"
	if insecure {
		scheme = "http"
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   registryHost,
	}

	base := u.String()

	return base
}

func splitKeppelRepository(repository string) (account, repo string, err error) {
	parts := strings.SplitN(repository, "/", 2)

	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		err := fmt.Errorf("invalid repository format %q, must be <account>/<repository-path>", repository)

		return "", "", err
	}

	account = parts[0]
	repo = parts[1]

	return account, repo, nil
}
