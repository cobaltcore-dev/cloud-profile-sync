// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testProvidersYAML = `
providers:
- name: alicloud
  versions:
  - version: 1.35.6
    classification: supported
- name: converged-cloud
  versions:
  - version: 1.31.4
    classification: supported
  - version: 1.32.1
    classification: deprecated
    expirationDate: '2027-06-10T23:59:59Z'
`

func TestParseProviderVersions(t *testing.T) {
	t.Run("selects the configured provider", func(t *testing.T) {
		versions, err := parseProviderVersions([]byte(testProvidersYAML), "converged-cloud")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(versions) != 2 {
			t.Fatalf("expected 2 versions, got %d", len(versions))
		}
		if versions[0].Version != "1.31.4" || versions[1].Version != "1.32.1" {
			t.Fatalf("unexpected versions: %+v", versions)
		}
		if versions[1].ExpirationDate == nil {
			t.Error("expected expiration date to be parsed")
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
	t.Run("errors on invalid yaml", func(t *testing.T) {
		_, err := parseProviderVersions([]byte("::: not yaml :::"), "converged-cloud")
		if err == nil {
			t.Fatal("expected parse error")
		}
	})
}

func TestGithubFetchKubernetesVersion(t *testing.T) {
	t.Run("errors when provider is empty", func(t *testing.T) {
		src := NewGithubKubernetesSource("http://example.invalid", "pat", "")
		_, err := src.FetchKubernetesVersion(context.Background())
		if err == nil || !strings.Contains(err.Error(), "provider must be set") {
			t.Fatalf("expected provider error, got %v", err)
		}
	})
	t.Run("fetches and parses versions over HTTP with auth", func(t *testing.T) {
		var gotAuth, gotAccept string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotAccept = r.Header.Get("Accept")
			_, err := w.Write([]byte(testProvidersYAML))
			if err != nil {
				t.Fatal(err)
			}
		}))
		defer srv.Close()

		src := NewGithubKubernetesSource(srv.URL, "my-token", "converged-cloud")
		versions, err := src.FetchKubernetesVersion(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(versions) != 2 {
			t.Fatalf("expected 2 versions, got %d", len(versions))
		}
		if gotAuth != "Bearer my-token" {
			t.Errorf("expected bearer token header, got %q", gotAuth)
		}
		if gotAccept != "application/vnd.github.raw" {
			t.Errorf("expected raw accept header, got %q", gotAccept)
		}
	})
	t.Run("returns the HTTP error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusForbidden)
		}))
		defer srv.Close()

		src := NewGithubKubernetesSource(srv.URL, "pat", "converged-cloud")
		_, err := src.FetchKubernetesVersion(context.Background())
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("expected 403 error, got %v", err)
		}
	})
}
