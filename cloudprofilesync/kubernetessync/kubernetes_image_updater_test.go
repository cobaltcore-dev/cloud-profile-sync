// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0
package kubernetessync

import (
	"context"
	"errors"
	"testing"
	"time"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeSource is a KubernetesVersionSource that returns a fixed list or error.
type fakeSource struct {
	versions []gardenerv1beta1.ExpirableVersion
	err      error
}

func (f *fakeSource) FetchVersions(_ context.Context) ([]gardenerv1beta1.ExpirableVersion, error) {
	return f.versions, f.err
}

func expiry(t time.Time) *metav1.Time { return &metav1.Time{Time: t} } //nolint:staticcheck

func TestKubernetesImageUpdater_Update(t *testing.T) {
	now := time.Now()

	t.Run("writes versions to CloudProfileSpec.Kubernetes", func(t *testing.T) {
		src := &fakeSource{versions: []gardenerv1beta1.ExpirableVersion{
			{Version: "1.31.0"},
			{Version: "1.32.0"},
		}}
		ku := NewKubernetesImageUpdater(src, 0)
		var spec gardenerv1beta1.CloudProfileSpec
		if err := ku.Update(context.Background(), &spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(spec.Kubernetes.Versions) != 2 {
			t.Fatalf("expected 2 versions, got %d", len(spec.Kubernetes.Versions))
		}
	})

	t.Run("keeps versions with no expiration date", func(t *testing.T) {
		src := &fakeSource{versions: []gardenerv1beta1.ExpirableVersion{
			{Version: "1.31.0"}, // no ExpirationDate
		}}
		ku := NewKubernetesImageUpdater(src, 30*24*time.Hour)
		var spec gardenerv1beta1.CloudProfileSpec
		if err := ku.Update(context.Background(), &spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(spec.Kubernetes.Versions) != 1 {
			t.Fatalf("expected 1 version, got %d", len(spec.Kubernetes.Versions))
		}
	})

	t.Run("drops version expired beyond threshold", func(t *testing.T) {
		src := &fakeSource{versions: []gardenerv1beta1.ExpirableVersion{
			{Version: "1.29.0", ExpirationDate: expiry(now.Add(-60 * 24 * time.Hour))}, //nolint:staticcheck
		}}
		ku := NewKubernetesImageUpdater(src, 30*24*time.Hour)
		var spec gardenerv1beta1.CloudProfileSpec
		spec.Kubernetes.Versions = []gardenerv1beta1.ExpirableVersion{{Version: "existing"}}
		err := ku.Update(context.Background(), &spec)
		if err == nil {
			t.Fatal("expected error when all versions filtered, got nil")
		}
		// CloudProfile must not have been modified.
		if len(spec.Kubernetes.Versions) != 1 || spec.Kubernetes.Versions[0].Version != "existing" {
			t.Errorf("spec was modified despite error: %v", spec.Kubernetes.Versions)
		}
	})

	t.Run("keeps version expired within threshold", func(t *testing.T) {
		src := &fakeSource{versions: []gardenerv1beta1.ExpirableVersion{
			{Version: "1.30.0", ExpirationDate: expiry(now.Add(-10 * 24 * time.Hour))}, //nolint:staticcheck
		}}
		ku := NewKubernetesImageUpdater(src, 30*24*time.Hour)
		var spec gardenerv1beta1.CloudProfileSpec
		if err := ku.Update(context.Background(), &spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(spec.Kubernetes.Versions) != 1 {
			t.Fatalf("expected 1 version, got %d", len(spec.Kubernetes.Versions))
		}
	})

	t.Run("mixed: keeps recent, drops stale", func(t *testing.T) {
		src := &fakeSource{versions: []gardenerv1beta1.ExpirableVersion{
			{Version: "1.31.0"},
			{Version: "1.30.0", ExpirationDate: expiry(now.Add(-10 * 24 * time.Hour))},  //nolint:staticcheck
			{Version: "1.29.0", ExpirationDate: expiry(now.Add(-60 * 24 * time.Hour))},  //nolint:staticcheck
		}}
		ku := NewKubernetesImageUpdater(src, 30*24*time.Hour)
		var spec gardenerv1beta1.CloudProfileSpec
		if err := ku.Update(context.Background(), &spec); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(spec.Kubernetes.Versions) != 2 {
			t.Fatalf("expected 2 versions, got %d", len(spec.Kubernetes.Versions))
		}
		got := map[string]bool{}
		for _, v := range spec.Kubernetes.Versions {
			got[v.Version] = true
		}
		if !got["1.31.0"] || !got["1.30.0"] {
			t.Errorf("unexpected versions in result: %v", spec.Kubernetes.Versions)
		}
		if got["1.29.0"] {
			t.Error("1.29.0 should have been filtered out")
		}
	})

	t.Run("returns error when source fails", func(t *testing.T) {
		src := &fakeSource{err: errors.New("upstream failure")}
		ku := NewKubernetesImageUpdater(src, 0)
		var spec gardenerv1beta1.CloudProfileSpec
		if err := ku.Update(context.Background(), &spec); err == nil {
			t.Fatal("expected error from source, got nil")
		}
	})

	t.Run("refuses to wipe CloudProfile when all versions filtered", func(t *testing.T) {
		src := &fakeSource{versions: []gardenerv1beta1.ExpirableVersion{
			{Version: "1.29.0", ExpirationDate: expiry(now.Add(-60 * 24 * time.Hour))}, //nolint:staticcheck
			{Version: "1.28.0", ExpirationDate: expiry(now.Add(-90 * 24 * time.Hour))}, //nolint:staticcheck
		}}
		ku := NewKubernetesImageUpdater(src, 30*24*time.Hour)
		var spec gardenerv1beta1.CloudProfileSpec
		spec.Kubernetes.Versions = []gardenerv1beta1.ExpirableVersion{{Version: "existing"}}
		err := ku.Update(context.Background(), &spec)
		if err == nil {
			t.Fatal("expected error when all versions filtered")
		}
		// CloudProfile must not have been modified.
		if len(spec.Kubernetes.Versions) != 1 || spec.Kubernetes.Versions[0].Version != "existing" {
			t.Errorf("spec was modified despite error: %v", spec.Kubernetes.Versions)
		}
	})
}
