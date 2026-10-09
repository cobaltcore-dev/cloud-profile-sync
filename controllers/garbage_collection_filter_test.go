// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

func versions(images []ossync.SourceImage) []string {
	out := make([]string, 0, len(images))
	for _, img := range images {
		out = append(out, img.Version)
	}
	return out
}

func TestFilter(t *testing.T) {
	now := time.Now()
	stale := now.Add(-48 * time.Hour) // before cutoff
	fresh := now.Add(-1 * time.Hour)  // after cutoff
	cutoff := now.Add(-24 * time.Hour)

	tests := []struct {
		name      string
		images    []ossync.SourceImage
		tags      map[string]time.Time
		protected map[string]struct{}
		want      []string
	}{
		{
			name:   "drops stale unreferenced",
			images: []ossync.SourceImage{{Version: "1.0.0"}, {Version: "2.0.0"}},
			tags:   map[string]time.Time{"1.0.0": stale, "2.0.0": fresh},
			want:   []string{"2.0.0"},
		},
		{
			name:      "keeps stale but referenced by version",
			images:    []ossync.SourceImage{{Version: "1.0.0"}},
			tags:      map[string]time.Time{"1.0.0": stale},
			protected: map[string]struct{}{"1.0.0": {}},
			want:      []string{"1.0.0"},
		},
		{
			name: "keeps all backing tags of a referenced clean version",
			images: []ossync.SourceImage{
				{Version: "2254.0.0-amd64", CleanVersion: "2254.0.0"},
				{Version: "2254.0.0-arm64", CleanVersion: "2254.0.0"},
			},
			tags:      map[string]time.Time{"2254.0.0-amd64": stale, "2254.0.0-arm64": stale},
			protected: map[string]struct{}{"2254.0.0": {}}, // Shoot pins the clean version
			want:      []string{"2254.0.0-amd64", "2254.0.0-arm64"},
		},
		{
			name: "drops unreferenced backing tags, keeps the one pinned by tag",
			images: []ossync.SourceImage{
				{Version: "2254.0.0-amd64", CleanVersion: "2254.0.0"},
				{Version: "2254.0.0-arm64", CleanVersion: "2254.0.0"},
			},
			tags:      map[string]time.Time{"2254.0.0-amd64": stale, "2254.0.0-arm64": stale},
			protected: map[string]struct{}{"2254.0.0-amd64": {}},
			want:      []string{"2254.0.0-amd64"},
		},
		{
			name:   "keeps fresh unreferenced",
			images: []ossync.SourceImage{{Version: "1.0.0"}},
			tags:   map[string]time.Time{"1.0.0": fresh},
			want:   []string{"1.0.0"},
		},
		{
			name:   "keeps version with unknown push time",
			images: []ossync.SourceImage{{Version: "1.0.0"}},
			tags:   map[string]time.Time{}, // 1.0.0 absent
			want:   []string{"1.0.0"},
		},
		{
			name:   "exactly at cutoff is kept (not strictly before)",
			images: []ossync.SourceImage{{Version: "1.0.0"}},
			tags:   map[string]time.Time{"1.0.0": cutoff},
			want:   []string{"1.0.0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &Filter{Tags: tc.tags, Protected: tc.protected, Cutoff: cutoff}
			got := versions(f.Filter(tc.images))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestFilterDoesNotMutateInput(t *testing.T) {
	images := []ossync.SourceImage{{Version: "1.0.0"}, {Version: "2.0.0"}}
	f := &Filter{
		Tags:   map[string]time.Time{"1.0.0": time.Now().Add(-48 * time.Hour)},
		Cutoff: time.Now().Add(-24 * time.Hour),
	}
	_ = f.Filter(images)
	if len(images) != 2 || images[0].Version != "1.0.0" || images[1].Version != "2.0.0" {
		t.Fatalf("input slice was mutated: %v", versions(images))
	}
}
