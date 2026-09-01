// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package landscape

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ocirepo"
)

// componentDescriptorFile is the file in the OCI artifact layer that holds
// the OCM component descriptor.
const componentDescriptorFile = "component-descriptor.yaml"

// kubeAPIServerResourceName is the component resource whose versions are used
// as Kubernetes versions.
const kubeAPIServerResourceName = "kube-apiserver"

// kubernetesVersionDataResourceName is the OCM resource that embeds
// KUBERNETES_VERSIONS.yaml as a localBlob.
const kubernetesVersionDataResourceName = "kubernetes-version-data"

// resourceAccess is the minimal shape of a resource's access stanza.
type resourceAccess struct {
	Type           string `yaml:"type"`
	LocalReference string `yaml:"localReference"`
	MediaType      string `yaml:"mediaType"`
	Size           int64  `yaml:"size"`
}

// componentDescriptor is the minimal shape of component-descriptor.yaml.
type componentDescriptor struct {
	Component struct {
		Resources []struct {
			Name    string         `yaml:"name"`
			Version string         `yaml:"version"`
			Access  resourceAccess `yaml:"access"`
		} `yaml:"resources"`
	} `yaml:"component"`
}

// yamlExpirableVersion is a YAML-unmarshalling intermediate for entries in the
// versions file. metav1.Time has no UnmarshalYAML, so we use *time.Time
// here and convert to gardenerv1beta1.ExpirableVersion after parsing.
type yamlExpirableVersion struct {
	Version        string                                 `yaml:"version"`
	Classification *gardenerv1beta1.VersionClassification `yaml:"classification"`
	ExpirationDate *time.Time                             `yaml:"expirationDate"`
}

// kubernetesVersions is the shape of the versions file (KUBERNETES_VERSIONS.yaml).
type kubernetesVersions struct {
	Providers []struct {
		Name     string                 `yaml:"name"`
		Versions []yamlExpirableVersion `yaml:"versions"`
	} `yaml:"providers"`
}

// LandscapeKubernetesSource fetches Kubernetes versions exclusively from the
// OCI component descriptor and its embedded kubernetes-version-data localBlob.
type LandscapeKubernetesSource struct {
	ociRepo  *remote.Repository
	provider string
}

// NewLandscapeKubernetesSource creates a source that fetches Kubernetes
// versions and their classifications from the OCI component descriptor.
// provider must match a provider name in the kubernetes-version-data blob.
func NewLandscapeKubernetesSource(ociParams ocirepo.Params, provider string) (*LandscapeKubernetesSource, error) {
	if provider == "" {
		return nil, errors.New("provider must be set")
	}
	repo, err := ocirepo.New(ociParams)
	if err != nil {
		return nil, fmt.Errorf("initializing OCI repository: %w", err)
	}
	return &LandscapeKubernetesSource{
		ociRepo:  repo,
		provider: provider,
	}, nil
}

// FetchVersions resolves the latest OCI tag, fetches the component descriptor
// to get supported versions, fetches the kubernetes-version-data localBlob for
// classifications at the same tag, and returns the intersection as
// []gardenerv1beta1.ExpirableVersion.
func (s *LandscapeKubernetesSource) FetchVersions(ctx context.Context) ([]gardenerv1beta1.ExpirableVersion, error) {
	tag, err := s.LatestTag(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving latest tag: %w", err)
	}

	supportedVersions, err := s.fetchSupportedVersions(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("fetching supported versions: %w", err)
	}

	classification, err := s.fetchClassification(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("fetching classification: %w", err)
	}

	supported := make(map[string]bool, len(supportedVersions))
	for _, v := range supportedVersions {
		supported[v] = true
	}

	result := make([]gardenerv1beta1.ExpirableVersion, 0, len(classification))
	for _, v := range classification {
		if !supported[v.Version] {
			continue
		}
		result = append(result, v)
	}
	return result, nil
}

// LatestTag returns the highest semver tag in the OCI repository.
// Tags that cannot be parsed as semver are ignored.
func (s *LandscapeKubernetesSource) LatestTag(ctx context.Context) (string, error) {
	var tags []string
	appendTagsFunc := func(newTags []string) error {
		tags = append(tags, newTags...)
		return nil
	}

	if err := s.ociRepo.Tags(ctx, "", appendTagsFunc); err != nil {
		return "", fmt.Errorf("listing tags: %w", err)
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("no tags found in %s", s.ociRepo.Reference)
	}

	type semverTag struct {
		raw string
		ver semver.Version
	}
	var parseable []semverTag
	for _, t := range tags {
		if v, err := semver.ParseTolerant(t); err == nil {
			parseable = append(parseable, semverTag{raw: t, ver: v})
		}
	}
	if len(parseable) == 0 {
		return "", fmt.Errorf("no semver tags found in %s", s.ociRepo.Reference)
	}

	latest := slices.MaxFunc(parseable, func(a, b semverTag) int {
		return a.ver.Compare(b.ver)
	})
	return latest.raw, nil
}

// fetchSupportedVersions returns the kube-apiserver version strings from the
// component descriptor at the given OCI tag.
func (s *LandscapeKubernetesSource) fetchSupportedVersions(ctx context.Context, tag string) ([]string, error) {
	cd, err := s.fetchComponentDescriptor(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("tag %s: %w", tag, err)
	}

	versions := make([]string, 0, len(cd.Component.Resources))
	for _, res := range cd.Component.Resources {
		if res.Name == kubeAPIServerResourceName {
			versions = append(versions, res.Version)
		}
	}

	return versions, nil
}

// fetchKubernetesVersionsBlob reads the component descriptor at the given OCI
// tag, locates the kubernetes-version-data resource, and fetches its localBlob
// content from the same OCI repository. The raw YAML bytes are returned.
func (s *LandscapeKubernetesSource) fetchKubernetesVersionsBlob(ctx context.Context, tag string) ([]byte, error) {
	cd, err := s.fetchComponentDescriptor(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("fetching component descriptor: %w", err)
	}

	for _, res := range cd.Component.Resources {
		if res.Name != kubernetesVersionDataResourceName {
			continue
		}
		if res.Access.Type != "localBlob/v1" {
			return nil, fmt.Errorf("resource %q has unexpected access type %q, want localBlob/v1", kubernetesVersionDataResourceName, res.Access.Type)
		}
		localRef := res.Access.LocalReference
		if localRef == "" {
			return nil, fmt.Errorf("resource %q has empty localReference", kubernetesVersionDataResourceName)
		}
		rc, err := s.ociRepo.Blobs().Fetch(ctx, ocispec.Descriptor{
			Digest:    digest.Digest(localRef),
			Size:      res.Access.Size,
			MediaType: res.Access.MediaType,
		})
		if err != nil {
			return nil, fmt.Errorf("fetching blob %s: %w", localRef, err)
		}
		defer rc.Close()
		raw, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("reading blob %s: %w", localRef, err)
		}
		return raw, nil
	}
	return nil, fmt.Errorf("resource %q not found in component descriptor at tag %s", kubernetesVersionDataResourceName, tag)
}

func (s *LandscapeKubernetesSource) fetchComponentDescriptor(ctx context.Context, tag string) (*componentDescriptor, error) {
	_, manifestBytes, err := oras.FetchBytes(ctx, s.ociRepo, tag, oras.DefaultFetchBytesOptions)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest: %w", err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	if len(manifest.Layers) == 0 {
		return nil, errors.New("manifest has no layers")
	}

	layerBytes, err := content.FetchAll(ctx, s.ociRepo, manifest.Layers[0])
	if err != nil {
		return nil, fmt.Errorf("fetching layer blob: %w", err)
	}

	cd, err := extractComponentDescriptor(bytes.NewReader(layerBytes))
	if err != nil {
		return nil, fmt.Errorf("extracting component descriptor: %w", err)
	}

	return cd, nil
}

func extractComponentDescriptor(r io.Reader) (*componentDescriptor, error) {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s not found in layer", componentDescriptorFile)
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if !matchesFile(hdr.Name, componentDescriptorFile) {
			continue
		}
		raw, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", componentDescriptorFile, err)
		}
		var cd componentDescriptor
		if err := yaml.Unmarshal(raw, &cd); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", componentDescriptorFile, err)
		}
		return &cd, nil
	}
}

func matchesFile(name, target string) bool {
	name = strings.TrimPrefix(name, "./")
	return name == target || strings.HasSuffix(name, "/"+target)
}

// fetchClassification fetches the kubernetes-version-data localBlob at the
// given tag and returns the versions for the configured provider.
func (s *LandscapeKubernetesSource) fetchClassification(ctx context.Context, tag string) ([]gardenerv1beta1.ExpirableVersion, error) {
	if s.provider == "" {
		return nil, errors.New("provider must be set")
	}
	raw, err := s.fetchKubernetesVersionsBlob(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("fetching kubernetes versions blob: %w", err)
	}
	return parseProviderVersions(raw, s.provider)
}

func parseProviderVersions(raw []byte, provider string) ([]gardenerv1beta1.ExpirableVersion, error) {
	var kv kubernetesVersions
	if err := yaml.Unmarshal(raw, &kv); err != nil {
		return nil, fmt.Errorf("parsing versions file: %w", err)
	}
	for _, p := range kv.Providers {
		if p.Name == provider {
			if len(p.Versions) == 0 {
				return nil, fmt.Errorf("provider %q has no versions", provider)
			}
			result := make([]gardenerv1beta1.ExpirableVersion, 0, len(p.Versions))
			for _, v := range p.Versions {
				result = append(result, gardenerv1beta1.ExpirableVersion{
					Version:        v.Version,
					Classification: v.Classification,                        //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
					ExpirationDate: convertExpirationDate(v.ExpirationDate), //nolint:staticcheck // legacy fields; Lifecycle needs the VersionClassificationLifecycle feature gate
				})
			}

			return result, nil
		}
	}
	return nil, fmt.Errorf("provider %q not found in the fetched data", provider)
}

func convertExpirationDate(t *time.Time) *metav1.Time {
	if t == nil {
		return nil
	}
	return &metav1.Time{Time: *t}
}
