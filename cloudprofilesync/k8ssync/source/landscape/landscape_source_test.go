// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package landscape

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/distribution/distribution/v3/configuration"
	"github.com/distribution/distribution/v3/registry"
	_ "github.com/distribution/distribution/v3/registry/storage/driver/inmemory"
	. "github.com/onsi/gomega"
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

// descriptorYAMLWithBlob builds a component descriptor YAML that includes both
// kube-apiserver resources and a kubernetes-version-data localBlob resource
// pointing to the given digest and size.
func descriptorYAMLWithBlob(t *testing.T, versionsDigest string, versionsSize int) string {
	t.Helper()
	return fmt.Sprintf(`
component:
  name: landscape-setup
  resources:
  - name: kube-apiserver
    version: 1.31.4
  - name: kube-apiserver
    version: 1.32.1
  - name: kubelet
    version: 1.31.4
  - name: kubernetes-version-data
    version: v1.2.3
    type: gardener.cloud/kubernetes-versions+yaml
    access:
      type: localBlob/v1
      localReference: %s
      mediaType: application/vnd.gardener.cloud/kubernetes-versions+yaml
      size: %d
`, versionsDigest, versionsSize)
}

// ---- OCI helpers ----

// freePort returns a TCP port number that is free at call time.
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

// startRegistry spins up an in-process distribution registry on addr.
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

// pushArtifactWithVersionsBlob pushes an OCI artifact that contains:
//   - a tar layer with the component descriptor (including a kubernetes-version-data
//     resource pointing to the versions blob by digest)
//   - the raw versions YAML as a standalone OCI blob
func pushArtifactWithVersionsBlob(t *testing.T, addr, repoName, tag, versionsYAML string) {
	t.Helper()
	ctx := context.Background()

	repo, err := remote.NewRepository(addr + "/" + repoName)
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	repo.PlainHTTP = true

	// Push the versions YAML blob first; compute its digest.
	versionsBlobBytes := []byte(versionsYAML)
	h := sha256.Sum256(versionsBlobBytes)
	versionsDigest := fmt.Sprintf("sha256:%x", h)
	versionsSize := len(versionsBlobBytes)
	versionsDesc := content.NewDescriptorFromBytes("application/vnd.gardener.cloud/kubernetes-versions+yaml", versionsBlobBytes)
	if err := repo.Push(ctx, versionsDesc, bytes.NewReader(versionsBlobBytes)); err != nil {
		t.Fatalf("push versions blob: %v", err)
	}

	// Build the component descriptor YAML referencing the versions blob.
	descriptorYAML := descriptorYAMLWithBlob(t, versionsDigest, versionsSize)

	// Build the tar layer containing the component descriptor.
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
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageManifest,
		Config:        ocispec.DescriptorEmptyJSON,
		Layers:        []ocispec.Descriptor{layerDesc, versionsDesc},
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

// pushComponentDescriptorArtifact pushes a minimal OCI artifact whose first
// layer is a tar containing componentDescriptorFile with the given YAML body.
// Use this for tests that do not need the versions blob.
func pushComponentDescriptorArtifact(t *testing.T, addr, repoName, tag, descriptorYAML string) {
	t.Helper()
	ctx := context.Background()

	repo, err := remote.NewRepository(addr + "/" + repoName)
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	repo.PlainHTTP = true

	layerBytes := tarWith(t, componentDescriptorFile, descriptorYAML)
	layerDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, layerBytes)
	if err := repo.Push(ctx, layerDesc, bytes.NewReader(layerBytes)); err != nil {
		t.Fatalf("push layer: %v", err)
	}

	if err := repo.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}")); err != nil {
		t.Fatalf("push config: %v", err)
	}

	manifest := ocispec.Manifest{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageManifest,
		Config:        ocispec.DescriptorEmptyJSON,
		Layers:        []ocispec.Descriptor{layerDesc},
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

// ---- unit tests ----

func TestExtractComponentDescriptor(t *testing.T) {
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
	tests := []struct {
		name         string
		tarName      string
		tarBody      string
		wantResCount int
		wantErr      string
	}{
		{
			name:         "parses resources from the tar",
			tarName:      componentDescriptorFile,
			tarBody:      testDescriptor,
			wantResCount: 3,
		},
		{
			name:         "tolerates a leading path prefix",
			tarName:      "landscape-setup/" + componentDescriptorFile,
			tarBody:      testDescriptor,
			wantResCount: 3,
		},
		{
			name:    "errors when the file is absent",
			tarName: "other-file.yaml",
			tarBody: "hello",
			wantErr: "not found in layer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			blob := tarWith(t, tc.tarName, tc.tarBody)
			cd, err := extractComponentDescriptor(bytes.NewReader(blob))
			if tc.wantErr != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tc.wantErr)))
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(cd.Component.Resources).To(HaveLen(tc.wantResCount))
		})
	}
	t.Run("errors on a non-tar blob", func(t *testing.T) {
		// Not a table entry: uses a non-tar io.Reader, not a tarWith blob.
		g := NewWithT(t)
		_, err := extractComponentDescriptor(strings.NewReader("not a tar"))
		g.Expect(err).To(HaveOccurred())
	})
}

func TestParseProviderVersions(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		provider   string
		wantCount  int
		wantExpiry bool // true if versions[1] must have a non-nil ExpirationDate
		wantErr    string
	}{
		{
			name:       "selects the configured provider",
			raw:        testProvidersYAML,
			provider:   "converged-cloud",
			wantCount:  3,
			wantExpiry: true,
		},
		{
			name:     "errors for an unknown provider",
			raw:      testProvidersYAML,
			provider: "gcp",
			wantErr:  "not found",
		},
		{
			name:     "errors when the provider has no versions",
			raw:      "providers:\n- name: empty\n  versions: []\n",
			provider: "empty",
			wantErr:  "no versions",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			versions, err := parseProviderVersions([]byte(tc.raw), tc.provider)
			if tc.wantErr != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tc.wantErr)))
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(versions).To(HaveLen(tc.wantCount))
			if tc.wantExpiry {
				g.Expect(versions[1].ExpirationDate).ToNot(BeNil()) //nolint:staticcheck
			}
		})
	}
}

// TestFetchKubernetesVersionsBlob tests fetchKubernetesVersionsBlob directly.
func TestFetchKubernetesVersionsBlob(t *testing.T) {
	addr := freePort(t)
	stop := startRegistry(t, addr)
	defer stop()

	tests := []struct {
		name    string
		setup   func(t *testing.T) // pushes the artifact to addr
		repo    string
		wantStr string // non-empty: raw bytes must contain this string
		wantErr string // non-empty: error must contain this string
	}{
		{
			name: "returns versions YAML from localBlob",
			setup: func(t *testing.T) {
				pushArtifactWithVersionsBlob(t, addr, "blob-ok", "v1.0.0", testProvidersYAML)
			},
			repo:    "blob-ok",
			wantStr: "converged-cloud",
		},
		{
			name: "errors when kubernetes-version-data resource is absent",
			setup: func(t *testing.T) {
				pushComponentDescriptorArtifact(t, addr, "blob-missing", "v1.0.0", `
component:
  name: landscape-setup
  resources:
  - name: kube-apiserver
    version: 1.31.4
`)
			},
			repo:    "blob-missing",
			wantErr: kubernetesVersionDataResourceName,
		},
		{
			name: "errors when access type is not localBlob/v1",
			setup: func(t *testing.T) {
				pushComponentDescriptorArtifact(t, addr, "blob-wrongtype", "v1.0.0", `
component:
  name: landscape-setup
  resources:
  - name: kubernetes-version-data
    version: v1.0.0
    access:
      type: ociBlob/v1
      localReference: sha256:abc123
`)
			},
			repo:    "blob-wrongtype",
			wantErr: "unexpected access type",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			tc.setup(t)
			ociRepo, err := remote.NewRepository(addr + "/" + tc.repo)
			g.Expect(err).ToNot(HaveOccurred())
			ociRepo.PlainHTTP = true

			src := &LandscapeKubernetesSource{ociRepo: ociRepo, provider: "converged-cloud"}
			raw, err := src.fetchKubernetesVersionsBlob(context.Background(), "v1.0.0")
			if tc.wantErr != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tc.wantErr)))
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(string(raw)).To(ContainSubstring(tc.wantStr))
		})
	}
}

// TestFetchVersions_EndToEnd exercises the full FetchVersions code path using
// a real in-process OCI registry. The component descriptor and the
// kubernetes-version-data localBlob are both served from the same registry.
// It verifies that only versions present in both the kube-apiserver resources
// and the classifications blob are returned.
func TestFetchVersions_EndToEnd(t *testing.T) {
	addr := freePort(t)
	stop := startRegistry(t, addr)
	defer stop()

	// OCI descriptor contains 1.31.4 and 1.32.1; versions blob has 1.33.0 too.
	// 1.33.0 must be filtered out because it is absent from kube-apiserver resources.
	pushArtifactWithVersionsBlob(t, addr, "k8s-versions", "v1.2.3", testProvidersYAML)

	ociRepo, err := remote.NewRepository(addr + "/k8s-versions")
	if err != nil {
		t.Fatalf("new oci repo: %v", err)
	}
	ociRepo.PlainHTTP = true

	src := &LandscapeKubernetesSource{ociRepo: ociRepo, provider: "converged-cloud"}

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
		t.Error("1.33.0 should not be in result (absent from kube-apiserver resources)")
	}
}

// TestFetchVersions_NoSemverTags verifies that LatestTag returns an error when
// the OCI repository has no semver-parseable tags.
func TestFetchVersions_NoSemverTags(t *testing.T) {
	addr := freePort(t)
	stop := startRegistry(t, addr)
	defer stop()

	pushComponentDescriptorArtifact(t, addr, "k8s-nosemver", "not-a-version", "component:\n  name: x\n  resources: []\n")

	ociRepo, err := remote.NewRepository(addr + "/k8s-nosemver")
	if err != nil {
		t.Fatalf("new oci repo: %v", err)
	}
	ociRepo.PlainHTTP = true

	src := &LandscapeKubernetesSource{ociRepo: ociRepo, provider: "converged-cloud"}
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

	// Build an artifact where the versions blob is present but there are no
	// kube-apiserver resources — the intersection must be empty.
	versionsBlobBytes := []byte(testProvidersYAML)
	h := sha256.Sum256(versionsBlobBytes)
	versionsDigest := fmt.Sprintf("sha256:%x", h)
	versionsSize := len(versionsBlobBytes)
	descriptorYAML := fmt.Sprintf(`
component:
  name: landscape-setup
  resources:
  - name: kubelet
    version: 1.31.4
  - name: kubernetes-version-data
    version: v1.0.0
    access:
      type: localBlob/v1
      localReference: %s
      mediaType: application/vnd.gardener.cloud/kubernetes-versions+yaml
      size: %d
`, versionsDigest, versionsSize)

	ctx := context.Background()
	repo2, err := remote.NewRepository(addr + "/k8s-noapiserver")
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}
	repo2.PlainHTTP = true
	vDesc := content.NewDescriptorFromBytes("application/vnd.gardener.cloud/kubernetes-versions+yaml", versionsBlobBytes)
	if err := repo2.Push(ctx, vDesc, bytes.NewReader(versionsBlobBytes)); err != nil {
		t.Fatalf("push versions blob: %v", err)
	}
	layerBytes := tarWith(t, componentDescriptorFile, descriptorYAML)
	layerDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, layerBytes)
	if err := repo2.Push(ctx, layerDesc, bytes.NewReader(layerBytes)); err != nil {
		t.Fatalf("push layer: %v", err)
	}
	if err := repo2.Push(ctx, ocispec.DescriptorEmptyJSON, strings.NewReader("{}")); err != nil {
		t.Fatalf("push config: %v", err)
	}
	manifest := ocispec.Manifest{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageManifest,
		Config:        ocispec.DescriptorEmptyJSON,
		Layers:        []ocispec.Descriptor{layerDesc, vDesc},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, manifestBytes)
	if err := repo2.PushReference(ctx, manifestDesc, bytes.NewReader(manifestBytes), "v1.0.0"); err != nil {
		t.Fatalf("push manifest: %v", err)
	}

	ociRepo, err := remote.NewRepository(addr + "/k8s-noapiserver")
	if err != nil {
		t.Fatalf("new oci repo: %v", err)
	}
	ociRepo.PlainHTTP = true

	src := &LandscapeKubernetesSource{ociRepo: ociRepo, provider: "converged-cloud"}
	versions, err := src.FetchVersions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected empty result when no kube-apiserver resources, got %v", versions)
	}
}
