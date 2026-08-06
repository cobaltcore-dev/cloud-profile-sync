// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package landscape

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync/source/oci"
)

// componentDescriptorFile is the file in the OCI artifact layer that holds
// the OCM component descriptor.
const componentDescriptorFile = "component-descriptor.yaml"

// kubeAPIServerResourceName is the component resource whose versions are used
// as Kubernetes versions.
const kubeAPIServerResourceName = "kube-apiserver"

// componentDescriptor is the minimal shape of component-descriptor.yaml.
type componentDescriptor struct {
	Component struct {
		Resources []struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		} `yaml:"resources"`
	} `yaml:"component"`
}

// yamlExpirableVersion is a YAML-unmarshalling intermediate for entries in the
// GitHub versions file. metav1.Time has no UnmarshalYAML, so we use *time.Time
// here and convert to gardenerv1beta1.ExpirableVersion after parsing.
type yamlExpirableVersion struct {
	Version        string                                `yaml:"version"`
	Classification gardenerv1beta1.VersionClassification `yaml:"classification"`
	ExpirationDate *time.Time                            `yaml:"expirationDate"`
}

// kubernetesVersions is the shape of the GitHub versions file.
type kubernetesVersions struct {
	Providers []struct {
		Name     string                 `yaml:"name"`
		Versions []yamlExpirableVersion `yaml:"versions"`
	} `yaml:"providers"`
}

// GithubParams configures the GitHub classification source.
type GithubParams struct {
	// RepositoryApiURL is the GitHub REST API base URL,
	// e.g. "https://api.github.com" or "https://github.mycompany.com/api/v3".
	RepositoryApiURL string
	// Repository is the owner/repo path, e.g. "my-org/landscape-setup".
	Repository string
	// FilePath is the path to the versions YAML file within the repository.
	FilePath string
	// Provider is the provider name to select from the versions file.
	Provider string

	// Transport is an optional custom HTTP transport for the GitHub client.
	Transport http.RoundTripper
}

// LandscapeKubernetesSource fetches Kubernetes versions from a Keppel OCI
// registry and their classifications from a GitHub repository, returning the
// intersection as []gardenerv1beta1.ExpirableVersion.
type LandscapeKubernetesSource struct {
	ociRepo      *remote.Repository
	githubClient *http.Client
	fileURL      string
	provider     string
}

func GithubPATTransport(apiBase, token string) http.RoundTripper {
	return &patTransport{token: token, base: http.DefaultTransport}
}

func GithubAppTransport(apiBase string, appID, installationID int64, privateKeyPEM []byte) (http.RoundTripper, error) {
	key, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}
	return &githubAppTransport{
		appID:          appID,
		installationID: installationID,
		apiBase:        apiBase,
		key:            key,
		base:           http.DefaultTransport,
	}, nil
}

func NewLandscapeKubernetesSource(ociParams oci.Params, gh GithubParams) (*LandscapeKubernetesSource, error) {
	if gh.RepositoryApiURL == "" {
		return nil, errors.New("repositoryApiUrl must be set")
	}
	repo, err := oci.NewRepository(ociParams)
	if err != nil {
		return nil, fmt.Errorf("initializing OCI repository: %w", err)
	}
	fileURL, err := contentsURL(gh.RepositoryApiURL, gh.Repository, gh.FilePath)
	if err != nil {
		return nil, fmt.Errorf("building github contents URL: %w", err)
	}
	return &LandscapeKubernetesSource{
		ociRepo:      repo,
		githubClient: &http.Client{Transport: gh.Transport},
		fileURL:      fileURL,
		provider:     gh.Provider,
	}, nil
}

// FetchVersions resolves the latest OCI tag, fetches the component descriptor
// to get supported versions, fetches the GitHub classification file at the same
// tag, and returns the intersection as []gardenerv1beta1.ExpirableVersion.
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

// fetchClassification downloads the versions YAML from GitHub at the given ref
// and returns the versions for the configured provider.
func (s *LandscapeKubernetesSource) fetchClassification(ctx context.Context, ref string) ([]gardenerv1beta1.ExpirableVersion, error) {
	if s.provider == "" {
		return nil, errors.New("provider must be set")
	}
	raw, err := s.fetchGithubFile(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("fetch github file: %w", err)
	}
	return parseProviderVersions(raw, s.provider)
}

func (s *LandscapeKubernetesSource) fetchGithubFile(ctx context.Context, ref string) ([]byte, error) {
	url := s.fileURL
	if ref != "" {
		url += "?ref=" + ref
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.raw")

	resp, err := s.githubClient.Do(req)
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
					Classification: &v.Classification,
					ExpirationDate: convertExpirationDate(v.ExpirationDate),
				})
			}

			return result, nil
		}
	}
	return nil, fmt.Errorf("provider %q not found in the fetched data", provider)
}

func contentsURL(apiURL, repo, filePath string) (string, error) {
	return url.JoinPath(apiURL, "repos", repo, "contents", filePath)
}

func convertExpirationDate(t *time.Time) *metav1.Time {
	if t == nil {
		return nil
	}
	return &metav1.Time{Time: *t}
}

// ---- GitHub PAT transport ----

type patTransport struct {
	token string
	base  http.RoundTripper
}

func (t *patTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

// ---- GitHub App transport ----

const tokenExpiryMargin = 5 * time.Minute

type githubAppTransport struct {
	appID          int64
	installationID int64
	apiBase        string
	key            *rsa.PrivateKey
	base           http.RoundTripper

	mu        sync.Mutex
	cached    string
	expiresAt time.Time
}

func (t *githubAppTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.installationToken(req.Context())
	if err != nil {
		return nil, fmt.Errorf("getting installation token: %w", err)
	}
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(r)
}

func (t *githubAppTransport) installationToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cached != "" && time.Now().Add(tokenExpiryMargin).Before(t.expiresAt) {
		return t.cached, nil
	}
	jwt, err := t.mintJWT()
	if err != nil {
		return "", fmt.Errorf("minting JWT: %w", err)
	}
	token, expiresAt, err := exchangeInstallationToken(ctx, t.base, t.apiBase, jwt, t.installationID)
	if err != nil {
		return "", err
	}
	t.cached = token
	t.expiresAt = expiresAt
	return token, nil
}

func (t *githubAppTransport) mintJWT() (string, error) {
	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString(mustJSON(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
	}))
	payload := base64.RawURLEncoding.EncodeToString(mustJSON(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": t.appID,
	}))

	sigInput := header + "." + payload
	h := sha256.New()
	h.Write([]byte(sigInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, t.key, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}
	return sigInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func exchangeInstallationToken(ctx context.Context, base http.RoundTripper, apiBase, jwt string, installationID int64) (string, time.Time, error) {
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", apiBase, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := base.RoundTrip(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("requesting installation token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("reading token response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("github returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", time.Time{}, fmt.Errorf("decoding token response: %w", err)
	}
	if result.Token == "" {
		return "", time.Time{}, errors.New("empty token in response")
	}
	return result.Token, result.ExpiresAt, nil
}

func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustJSON: %v", err))
	}
	return b
}
