// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package gc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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
func NormalizeTag(tag string) string {
	return strings.ReplaceAll(tag, "_", "+")
}

func FetchKeppelTags(ctx context.Context, registry, repository string) (map[string]time.Time, error) {
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
		return nil, fmt.Errorf("keppel API returned status %d", resp.StatusCode)
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

	return fmt.Sprintf(
		"%s/keppel/v1/accounts/%s/repositories/%s/_manifests",
		baseURL,
		account,
		repo,
	), nil
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

	return u.String()
}

func splitKeppelRepository(repository string) (account, repo string, err error) {
	parts := strings.SplitN(repository, "/", 2)

	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repository format %q, must be <account>/<repository-path>", repository)
	}

	return parts[0], parts[1], nil
}
