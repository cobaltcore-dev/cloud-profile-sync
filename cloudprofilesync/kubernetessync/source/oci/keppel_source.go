// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"archive/tar"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.yaml.in/yaml/v3"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/kubernetessync"
	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/source/oci"
)

// componentDescriptorFile is the file in the artifact's first layer that holds
// the OCM component descriptor.
const componentDescriptorFile = "component-descriptor.yaml"

// defaultKeppelResourceName is the component resource whose versions are used as
// Kubernetes versions when none is configured.
const defaultKeppelResourceName = "kube-apiserver"

// componentDescriptor is the minimal shape of component-descriptor.yaml needed
// to extract resource versions, mirroring `.component.resources[]` from the
// crane-based script.
type componentDescriptor struct {
	Component struct {
		Resources []struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		} `yaml:"resources"`
	} `yaml:"component"`
}

// KeppelKubernetesSource fetches Kubernetes versions from an OCM component
// artifact in a Keppel registry. It reads the latest tag, extracts
// component-descriptor.yaml from the artifact's first layer and returns the
// versions of the configured resource (default: kube-apiserver).
type KeppelKubernetesSource struct {
	repo         *remote.Repository
	resourceName string
}

// KeppelParams configures a KeppelKubernetesSource.
type KeppelParams struct {
	Registry   string
	Repository string
	Username   string
	Password   string
	// ResourceName is the component resource whose versions are read.
	// Defaults to kube-apiserver when empty.
	ResourceName string
}

// NewKeppelKubernetesSource builds a KeppelKubernetesSource, reusing the same
// oras-go repository and auth setup as the OCI machine image source.
func NewKeppelKubernetesSource(params KeppelParams, insecure bool) (*KeppelKubernetesSource, error) {
	repo, err := oci.NewRepository(oci.Params{
		Registry:   params.Registry,
		Repository: params.Repository,
		Username:   params.Username,
		Password:   params.Password,
		Insecure:   insecure,
	})
	if err != nil {
		return nil, err
	}

	resourceName := params.ResourceName
	if resourceName == "" {
		resourceName = defaultKeppelResourceName
	}

	return &KeppelKubernetesSource{
		repo:         repo,
		resourceName: resourceName,
	}, nil
}

// FetchKubernetesVersion implements KubernetesImageProvider. It resolves the
// latest tag, extracts the component-descriptor and returns the versions of the
// configured resource. The component-descriptor carries no classification or
// expiration, so all versions are classified as supported.
func (k *KeppelKubernetesSource) FetchKubernetesVersion(ctx context.Context) ([]kubernetessync.ExpirableVersion, error) {
	tag, err := k.latestTag(ctx)
	if err != nil {
		return nil, err
	}

	cd, err := k.fetchComponentDescriptor(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("tag %s: %w", tag, err)
	}

	versions := selectResourceVersions(cd, k.resourceName)
	if len(versions) == 0 {
		return nil, fmt.Errorf("tag %s: no versions found for resource %q", tag, k.resourceName)
	}

	return versions, nil
}

// selectResourceVersions returns the versions of the resources named
// resourceName in the component descriptor. The component-descriptor carries no
// classification or expiration, so all versions are classified as supported.
func selectResourceVersions(cd *componentDescriptor, resourceName string) []kubernetessync.ExpirableVersion {
	versions := make([]kubernetessync.ExpirableVersion, 0, len(cd.Component.Resources))
	for _, res := range cd.Component.Resources {
		if res.Name != resourceName {
			continue
		}
		versions = append(versions, kubernetessync.ExpirableVersion{
			Version:        res.Version,
			Classification: gardenerv1beta1.ClassificationSupported,
		})
	}
	return versions
}

// latestTag lists the repository tags and returns the highest one by semver,
// mirroring `crane ls | sort -rV | head -1`.
func (k *KeppelKubernetesSource) latestTag(ctx context.Context) (string, error) {
	var tags []string
	if err := k.repo.Tags(ctx, "", func(t []string) error {
		tags = append(tags, t...)
		return nil
	}); err != nil {
		return "", fmt.Errorf("listing tags: %w", err)
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("no tags found in %s", k.repo.Reference)
	}

	// Pick the highest tag by semver, falling back to lexical order when a tag
	// is not semver-parseable so the result stays deterministic.
	latest := slices.MaxFunc(tags, func(a, b string) int {
		va, ea := semver.ParseTolerant(a)
		vb, eb := semver.ParseTolerant(b)
		if ea != nil || eb != nil {
			return cmp.Compare(a, b)
		}
		return va.Compare(vb)
	})

	return latest, nil
}

// fetchComponentDescriptor fetches the artifact manifest for tag, pulls its
// first layer and extracts component-descriptor.yaml from the tar blob. This
// mirrors `crane manifest`, `crane blob` and `tar -xO` from the script.
func (k *KeppelKubernetesSource) fetchComponentDescriptor(ctx context.Context, tag string) (*componentDescriptor, error) {
	_, manifestBytes, err := oras.FetchBytes(ctx, k.repo, tag, oras.DefaultFetchBytesOptions)
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

	layerBytes, err := content.FetchAll(ctx, k.repo, manifest.Layers[0])
	if err != nil {
		return nil, fmt.Errorf("fetching layer blob: %w", err)
	}

	return extractComponentDescriptor(bytes.NewReader(layerBytes))
}

// extractComponentDescriptor scans a tar stream for component-descriptor.yaml
// and unmarshals it.
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

// matchesFile compares a tar entry name against target, tolerating a leading
// "./" and any leading path segments (some tools prefix a component root).
func matchesFile(name, target string) bool {
	name = strings.TrimPrefix(name, "./")
	return name == target || strings.HasSuffix(name, "/"+target)
}
