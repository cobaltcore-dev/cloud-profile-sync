// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
)

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

func TestSelectResourceVersions(t *testing.T) {
	cd, err := extractComponentDescriptor(bytes.NewReader(tarWith(t, componentDescriptorFile, testDescriptor)))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Run("selects only the matching resource", func(t *testing.T) {
		versions := selectResourceVersions(cd, "kube-apiserver")
		if len(versions) != 2 {
			t.Fatalf("expected 2 versions, got %d", len(versions))
		}
		got := []string{versions[0].Version, versions[1].Version}
		want := map[string]bool{"1.31.4": true, "1.32.1": true}
		for _, v := range got {
			if !want[v] {
				t.Errorf("unexpected version %q", v)
			}
		}
		for _, v := range versions {
			if v.Classification != gardenerv1beta1.ClassificationSupported {
				t.Errorf("expected classification supported, got %q", v.Classification)
			}
		}
	})
	t.Run("returns empty for an unknown resource", func(t *testing.T) {
		if got := selectResourceVersions(cd, "does-not-exist"); len(got) != 0 {
			t.Fatalf("expected no versions, got %d", len(got))
		}
	})
}
