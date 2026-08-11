// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package landscape

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
)

// ---- helpers ----

func tarWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func generateTestKey(t *testing.T) (key *rsa.PrivateKey, pemBytes []byte) {
	t.Helper()
	var err error
	key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	pemBytes = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, pemBytes
}

const testDescriptor = `
component:
  name: landscape-setup
  resources:
  - name: kube-apiserver
    version: 1.31.4
  - name: kube-apiserver
    version: 1.32.1
  - name: kubelet
    version: 1.31.4
`

const testProvidersYAML = `
providers:
- name: converged-cloud
  versions:
  - version: 1.31.4
    classification: supported
  - version: 1.32.1
    classification: deprecated
    expirationDate: '2027-06-10T23:59:59Z'
  - version: 1.33.0
    classification: supported
`

// ---- OCI helpers ----

func TestExtractComponentDescriptor(t *testing.T) {
	t.Run("parses resources from the tar", func(t *testing.T) {
		blob := tarWith(t, componentDescriptorFile, testDescriptor)
		cd, err := extractComponentDescriptor(bytes.NewReader(blob))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := len(cd.Component.Resources); got != 3 {
			t.Fatalf("expected 3 resources, got %d", got)
		}
	})
	t.Run("tolerates a leading path prefix", func(t *testing.T) {
		blob := tarWith(t, "landscape-setup/"+componentDescriptorFile, testDescriptor)
		if _, err := extractComponentDescriptor(bytes.NewReader(blob)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("errors when the file is absent", func(t *testing.T) {
		blob := tarWith(t, "other-file.yaml", "hello")
		_, err := extractComponentDescriptor(bytes.NewReader(blob))
		if err == nil || !strings.Contains(err.Error(), "not found in layer") {
			t.Fatalf("expected not-found error, got %v", err)
		}
	})
	t.Run("errors on a non-tar blob", func(t *testing.T) {
		_, err := extractComponentDescriptor(strings.NewReader("not a tar"))
		if err == nil {
			t.Fatal("expected error for non-tar blob")
		}
	})
}

// ---- GitHub classification helpers ----

func TestParseProviderVersions(t *testing.T) {
	t.Run("selects the configured provider", func(t *testing.T) {
		versions, err := parseProviderVersions([]byte(testProvidersYAML), "converged-cloud")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(versions) != 3 {
			t.Fatalf("expected 3 versions, got %d", len(versions))
		}
		if versions[1].ExpirationDate == nil { //nolint:staticcheck
			t.Error("expected expiration date to be parsed for deprecated version")
		}
	})
	t.Run("errors for an unknown provider", func(t *testing.T) {
		_, err := parseProviderVersions([]byte(testProvidersYAML), "gcp")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected not-found error, got %v", err)
		}
	})
	t.Run("errors when the provider has no versions", func(t *testing.T) {
		_, err := parseProviderVersions([]byte("providers:\n- name: empty\n  versions: []\n"), "empty")
		if err == nil || !strings.Contains(err.Error(), "no versions") {
			t.Fatalf("expected no-versions error, got %v", err)
		}
	})
}

// ---- GitHub App transport ----

func TestGithubAppTransport_MintJWT(t *testing.T) {
	key, _ := generateTestKey(t)
	tr := &githubAppTransport{appID: 42, installationID: 99, key: key, base: http.DefaultTransport}

	jwt, err := tr.mintJWT()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parts := strings.Split(jwt, "."); len(parts) != 3 {
		t.Fatalf("expected 3 JWT parts, got %d", len(parts))
	}
}

func TestGithubAppTransport_TokenCaching(t *testing.T) {
	key, _ := generateTestKey(t)

	tokenCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "access_tokens") {
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			resp := map[string]any{
				"token":      fmt.Sprintf("inst-token-%d", tokenCalls),
				"expires_at": time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				t.Error(err)
			}
			return
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(testProvidersYAML)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	tr := &githubAppTransport{
		appID:          42,
		installationID: 99,
		apiBase:        srv.URL,
		key:            key,
		base:           http.DefaultTransport,
	}

	// Two requests should produce only one token exchange call due to caching.
	for range 2 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, http.NoBody)
		if err != nil {
			t.Fatalf("creating request: %v", err)
		}
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
	}

	if tokenCalls != 1 {
		t.Errorf("expected 1 token exchange, got %d", tokenCalls)
	}
}

func TestParseRSAPrivateKey(t *testing.T) {
	t.Run("parses PKCS1 PEM", func(t *testing.T) {
		_, pemBytes := generateTestKey(t)
		if _, err := parseRSAPrivateKey(pemBytes); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("errors on non-PEM input", func(t *testing.T) {
		if _, err := parseRSAPrivateKey([]byte("not a pem")); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("errors on unsupported PEM type", func(t *testing.T) {
		b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")})
		if _, err := parseRSAPrivateKey(b); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("expected unsupported error, got %v", err)
		}
	})
}

// ---- GitHub fetch helpers ----

// TestFetchGithubFile_RefQueryParam verifies that fetchGithubFile appends
// the ?ref= query parameter when a ref is provided.
func TestFetchGithubFile_RefQueryParam(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if _, err := w.Write([]byte(testProvidersYAML)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	src := &LandscapeKubernetesSource{
		githubClient: &http.Client{Transport: &patTransport{token: "tok", base: http.DefaultTransport}},
		fileURL:      srv.URL,
		provider:     "converged-cloud",
	}
	if _, err := src.fetchClassification(context.Background(), "v1.2.3"); err != nil {
		t.Fatalf("fetchClassification: %v", err)
	}
	if gotQuery != "ref=v1.2.3" {
		t.Errorf("expected ref=v1.2.3 query param, got %q", gotQuery)
	}
}

// TestFetchGithubFile_NonOKStatus verifies that a non-200 GitHub response is
// propagated as an error containing the status code.
func TestFetchGithubFile_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	src := &LandscapeKubernetesSource{
		githubClient: &http.Client{},
		fileURL:      srv.URL,
		provider:     "converged-cloud",
	}
	_, err := src.fetchClassification(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}

// ---- FetchVersions end-to-end (real OCI registry + httptest GitHub) ----

// freePort returns a TCP port number that is free at call time. There is a
// small TOCTOU window but it is negligible for local test registries.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// startRegistry spins up an in-process distribution registry on addr and
// returns a cleanup function. It fails the test immediately if the registry
// does not become ready within 500 ms.
func startRegistry(t *testing.T, addr string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	reg, err := registry.NewRegistry(ctx, &configuration.Configuration{
		Storage:    configuration.Storage{"inmemory": map[string]any{}},
		HTTP:       configuration.HTTP{Addr: addr},
		Validation: configuration.Validation{Disabled: true},
		Log:        configuration.Log{Level: "error", AccessLog: configuration.AccessLog{Disabled: true}},
	})
	if err != nil {
		cancel()
		t.Fatalf("creating registry: %v", err)
	}
	go func() { _ = reg.ListenAndServe() }() //nolint:errcheck

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr, http.NoBody)
		if err != nil {
			cancel()
			t.Fatalf("building readiness request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("registry on %s did not become ready within 500ms", addr)
		}
	}
	return func() {
		cancel()
		_ = reg.Shutdown(context.Background()) //nolint:errcheck
	}
}

// pushComponentDescriptorArtifact pushes a minimal OCI artifact whose first
// layer is a tar containing componentDescriptorFile with the given YAML body,
// tagged with tag.
func pushComponentDescriptorArtifact(t *testing.T, addr, repoName, tag, descriptorYAML string) {
	t.Helper()
	ctx := context.Background()

	repo, err := remote.NewRepository(addr + "/" + repoName)
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	repo.PlainHTTP = true

	// Build the tar layer.
	layerBytes := tarWith(t, componentDescriptorFile, descriptorYAML)
	layerDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, layerBytes)
	if err := repo.Push(ctx, layerDesc, bytes.NewReader(layerBytes)); err != nil {
		t.Fatalf("push layer: %v", err)
	}

	// Push empty config blob.
	if err := repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}")); err != nil {
		t.Fatalf("push config: %v", err)
	}

	// Build and push the OCI manifest.
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    ocispec.DescriptorEmptyJSON,
		Layers:    []ocispec.Descriptor{layerDesc},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, manifestBytes)
	if err := repo.PushReference(ctx, manifestDesc, bytes.NewReader(manifestBytes), tag); err != nil {
		t.Fatalf("push manifest: %v", err)
	}
}

// TestFetchVersions_EndToEnd exercises the full FetchVersions code path: the
// OCI component descriptor is fetched from a real in-process registry; the
// GitHub classification file is served by an httptest.Server.
// It verifies that only versions present in both sources are returned.
func TestFetchVersions_EndToEnd(t *testing.T) {
	addr := freePort(t)
	stop := startRegistry(t, addr)
	defer stop()

	// OCI: descriptor contains 1.31.4 and 1.32.1 only (1.33.0 absent).
	descriptor := `
component:
  name: landscape-setup
  resources:
  - name: kube-apiserver
    version: 1.31.4
  - name: kube-apiserver
    version: 1.32.1
  - name: kubelet
    version: 1.31.4
`
	pushComponentDescriptorArtifact(t, addr, "k8s-versions", "v1.2.3", descriptor)

	// GitHub: has 1.31.4, 1.32.1, and 1.33.0 — 1.33.0 must be filtered out
	// because it is absent from the OCI component descriptor.
	githubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(testProvidersYAML)); err != nil {
			t.Error(err)
		}
	}))
	defer githubSrv.Close()

	fileURL, err := contentsURL(githubSrv.URL, "org/repo", "kubernetes/versions.yaml")
	if err != nil {
		t.Fatalf("contentsURL: %v", err)
	}

	ociRepo, err := remote.NewRepository(addr + "/k8s-versions")
	if err != nil {
		t.Fatalf("new oci repo: %v", err)
	}
	ociRepo.PlainHTTP = true

	src := &LandscapeKubernetesSource{
		ociRepo:      ociRepo,
		githubClient: &http.Client{},
		fileURL:      fileURL,
		provider:     "converged-cloud",
	}

	versions, err := src.FetchVersions(context.Background())
	if err != nil {
		t.Fatalf("FetchVersions: %v", err)
	}

	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d: %v", len(versions), versions)
	}
	got := make(map[string]bool, len(versions))
	for _, v := range versions {
		got[v.Version] = true
	}
	for _, want := range []string{"1.31.4", "1.32.1"} {
		if !got[want] {
			t.Errorf("expected version %q in result", want)
		}
	}
	if got["1.33.0"] {
		t.Error("1.33.0 should not be in result (absent from OCI component descriptor)")
	}
}

// TestFetchVersions_NoSemverTags verifies that LatestTag returns an error when
// the OCI repository has no semver-parseable tags.
func TestFetchVersions_NoSemverTags(t *testing.T) {
	addr := freePort(t)
	stop := startRegistry(t, addr)
	defer stop()

	// Push a single manifest under a non-semver tag.
	pushComponentDescriptorArtifact(t, addr, "k8s-nosemver", "not-a-version", testDescriptor)

	ociRepo, err := remote.NewRepository(addr + "/k8s-nosemver")
	if err != nil {
		t.Fatalf("new oci repo: %v", err)
	}
	ociRepo.PlainHTTP = true

	src := &LandscapeKubernetesSource{
		ociRepo:      ociRepo,
		githubClient: &http.Client{},
		fileURL:      "http://unused",
		provider:     "converged-cloud",
	}
	_, err = src.FetchVersions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no semver tags") {
		t.Fatalf("expected no-semver-tags error, got %v", err)
	}
}

// TestFetchVersions_NoKubeAPIServerResources verifies that when the component
// descriptor has no kube-apiserver resources, FetchVersions returns an empty
// intersection (no versions written to the CloudProfile).
func TestFetchVersions_NoKubeAPIServerResources(t *testing.T) {
	addr := freePort(t)
	stop := startRegistry(t, addr)
	defer stop()

	noAPIServerDescriptor := `
component:
  name: landscape-setup
  resources:
  - name: kubelet
    version: 1.31.4
`
	pushComponentDescriptorArtifact(t, addr, "k8s-noapiserver", "v1.0.0", noAPIServerDescriptor)

	githubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte(testProvidersYAML)); err != nil {
			t.Error(err)
		}
	}))
	defer githubSrv.Close()

	fileURL, err := contentsURL(githubSrv.URL, "org/repo", "kubernetes/versions.yaml")
	if err != nil {
		t.Fatalf("contentsURL: %v", err)
	}

	ociRepo, err := remote.NewRepository(addr + "/k8s-noapiserver")
	if err != nil {
		t.Fatalf("new oci repo: %v", err)
	}
	ociRepo.PlainHTTP = true

	src := &LandscapeKubernetesSource{
		ociRepo:      ociRepo,
		githubClient: &http.Client{},
		fileURL:      fileURL,
		provider:     "converged-cloud",
	}
	versions, err := src.FetchVersions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected empty result when no kube-apiserver resources, got %v", versions)
	}
}
