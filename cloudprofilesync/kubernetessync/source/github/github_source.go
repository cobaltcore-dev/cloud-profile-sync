// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"go.yaml.in/yaml/v3"
	"golang.org/x/oauth2"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/kubernetessync"
)

// kubernetesVersions is the shape of the GitHub versions file: a list of
// providers, each with its own list of expirable Kubernetes versions.
type kubernetesVersions struct {
	Providers []struct {
		Name     string                            `yaml:"name"`
		Versions []kubernetessync.ExpirableVersion `yaml:"versions"`
	} `yaml:"providers"`
}

// GithubKubernetesSource fetches Kubernetes versions from a YAML file in a
// GitHub repository, selecting the versions for a configured provider. The file
// has a providers[].versions[] shape.
type GithubKubernetesSource struct {
	url      string
	pat      string
	provider string
}

// NewGithubKubernetesSource builds a GithubKubernetesSource. url must point at a
// GitHub contents API endpoint for the versions file (the raw content is
// requested via the Accept header). provider selects which provider's versions
// to return and is required.
func NewGithubKubernetesSource(url, pat, provider string) *GithubKubernetesSource {
	return &GithubKubernetesSource{
		url:      url,
		pat:      pat,
		provider: provider,
	}
}

// FetchKubernetesVersion implements KubernetesImageProvider. It downloads the
// versions file and returns the versions for the configured provider.
func (gh *GithubKubernetesSource) FetchKubernetesVersion(ctx context.Context) ([]kubernetessync.ExpirableVersion, error) {
	if gh.provider == "" {
		return nil, errors.New("provider must be set")
	}

	raw, err := gh.fetchFile(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch github file: %w", err)
	}

	return parseProviderVersions(raw, gh.provider)
}

// parseProviderVersions parses a providers[].versions[] YAML document and
// returns the versions of the named provider.
func parseProviderVersions(raw []byte, provider string) ([]kubernetessync.ExpirableVersion, error) {
	var kv kubernetesVersions
	if err := yaml.Unmarshal(raw, &kv); err != nil {
		return nil, fmt.Errorf("parsing versions file: %w", err)
	}

	for _, p := range kv.Providers {
		if p.Name == provider {
			if len(p.Versions) == 0 {
				return nil, fmt.Errorf("provider %q has no versions", provider)
			}
			return p.Versions, nil
		}
	}

	return nil, fmt.Errorf("provider %q not found in the fetched data", provider)
}

func (gh *GithubKubernetesSource) fetchFile(ctx context.Context) ([]byte, error) {
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: gh.pat}))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gh.url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.raw")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("can't read body, github API returned %d: %w", resp.StatusCode, err)
		}

		return nil, fmt.Errorf("github API returned %d: %s", resp.StatusCode, body)
	}

	return io.ReadAll(resp.Body)
}
