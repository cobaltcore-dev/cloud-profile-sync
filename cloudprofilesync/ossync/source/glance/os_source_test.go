// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package glance

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

const (
	testRegion  = "eu-de-1"
	testVersion = "2150.8.0"
	// stdImage and usiImage are the standard and _usi variants of the same version.
	stdImage = "gardenlinux-openstack-gardener_prod-amd64-2150.8.0-40f62d58"
	usiImage = "gardenlinux-openstack-gardener_prod_usi-amd64-2150.8.0-40f62d58"
)

func TestParseVersionSkipsUsiVariant(t *testing.T) {
	g := newTestGlance(t, GlanceParams{Regions: []string{testRegion}}, nil)

	tests := []struct {
		name     string
		imgName  string
		wantVer  string
		wantKeep bool
	}{
		{
			name:     "standard image is parsed",
			imgName:  stdImage,
			wantVer:  testVersion,
			wantKeep: true,
		},
		{
			name:     "usi variant is skipped",
			imgName:  usiImage,
			wantKeep: false,
		},
		{
			name:     "usi variant with two-part version is skipped",
			imgName:  "gardenlinux-openstack-gardener_prod_usi-amd64-1877.13-81e502e7",
			wantKeep: false,
		},
		{
			name:     "unrelated image is skipped",
			imgName:  "some-other-image-1.2.3-deadbeef",
			wantKeep: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, keep := g.parseVersion(tc.imgName)
			if keep != tc.wantKeep {
				t.Fatalf("parseVersion(%q) keep = %v, want %v", tc.imgName, keep, tc.wantKeep)
			}
			if keep && got != tc.wantVer {
				t.Errorf("parseVersion(%q) = %q, want %q", tc.imgName, got, tc.wantVer)
			}
		})
	}
}

// When a version has both a standard and a usi image, only the standard one survives.
func TestGetVersionsUsiDoesNotCollide(t *testing.T) {
	imgs := []images.Image{
		{ID: "standard-uuid", Name: stdImage},
		{ID: "usi-uuid", Name: usiImage},
	}
	g := newTestGlance(t, GlanceParams{Regions: []string{testRegion}}, map[string][]images.Image{testRegion: imgs})

	versions, err := g.GetVersions(context.Background())
	if err != nil {
		t.Fatalf("GetVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("got %d versions, want 1 (usi must not create a second entry): %+v", len(versions), versions)
	}
	v := versions[0]
	if v.Version != testVersion {
		t.Errorf("version = %q, want %s", v.Version, testVersion)
	}
	if len(v.Regions) != 1 {
		t.Fatalf("got %d region entries, want 1 (usi must not duplicate the region)", len(v.Regions))
	}
	if v.Regions[0].ID != "standard-uuid" {
		t.Errorf("region ID = %q, want standard-uuid (usi UUID must not win)", v.Regions[0].ID)
	}
}

// When a region has two images for the same version (a rebuilt image with a new
// UUID), the newest active image is chosen deterministically.
func TestGetVersionsPicksCanonicalImage(t *testing.T) {
	older := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	imgs := []images.Image{
		{ID: "old-active", Name: stdImage, Status: images.ImageStatusActive, CreatedAt: older},
		{ID: "new-active", Name: stdImage, Status: images.ImageStatusActive, CreatedAt: newer},
		{ID: "new-queued", Name: stdImage, Status: images.ImageStatusQueued, CreatedAt: newer},
	}
	g := newTestGlance(t, GlanceParams{Regions: []string{testRegion}}, map[string][]images.Image{testRegion: imgs})

	versions, err := g.GetVersions(context.Background())
	if err != nil {
		t.Fatalf("GetVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("got %d versions, want 1: %+v", len(versions), versions)
	}
	v := versions[0]
	if len(v.Regions) != 1 {
		t.Fatalf("got %d region entries, want 1 (rebuilt image must not duplicate the region): %+v", len(v.Regions), v.Regions)
	}
	if v.Regions[0].ID != "new-active" {
		t.Errorf("region ID = %q, want new-active (newest active image must win)", v.Regions[0].ID)
	}
}

// preferImage selection is stable regardless of listing order.
func TestPreferImageDeterministic(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	active := images.Image{ID: "a", Status: images.ImageStatusActive, CreatedAt: base}
	queued := images.Image{ID: "z", Status: images.ImageStatusQueued, CreatedAt: base.Add(time.Hour)}
	if !preferImage(active, queued) {
		t.Error("active image should be preferred over a newer queued image")
	}
	if preferImage(queued, active) {
		t.Error("newer queued image should not be preferred over an active image")
	}

	// Same status and CreatedAt: larger UUID wins, both directions.
	lo := images.Image{ID: "aaa", Status: images.ImageStatusActive, CreatedAt: base}
	hi := images.Image{ID: "bbb", Status: images.ImageStatusActive, CreatedAt: base}
	if !preferImage(hi, lo) || preferImage(lo, hi) {
		t.Error("UUID tie-break is not deterministic")
	}
}

// The oldest version is marked deprecated and gets an expiration date stamped;
// newer (supported) versions have none.
func TestGetVersionsStampsExpirationOnDeprecated(t *testing.T) {
	imgs := []images.Image{
		{ID: "old-uuid", Name: "gardenlinux-openstack-gardener_prod-amd64-2150.8.0-40f62d58"},
		{ID: "new-uuid", Name: "gardenlinux-openstack-gardener_prod-amd64-2151.0.0-50f62d58"},
	}
	g := newTestGlance(t, GlanceParams{Regions: []string{testRegion}}, map[string][]images.Image{testRegion: imgs})

	versions, err := g.GetVersions(context.Background())
	if err != nil {
		t.Fatalf("GetVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want 2: %+v", len(versions), versions)
	}
	// versions is sorted newest-first, so the last entry is the deprecated one.
	newest, oldest := versions[0], versions[len(versions)-1]
	if oldest.ExpirationDate == nil {
		t.Error("deprecated version should have an expiration date stamped")
	}
	if newest.ExpirationDate != nil {
		t.Errorf("supported version should not have an expiration date, got %v", newest.ExpirationDate)
	}
}

// imageName builds a standard gardenlinux Glance image name for a version.
func imageName(version string) string {
	return defaultGlanceNamePrefix + version + "-deadbeef"
}

// versionStrings extracts the ordered version strings from the result.
func versionStrings(vs []ossync.SourceImage) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Version
	}
	return out
}

// VersionOffset selects a slice of the newest-first versions, skipping the first
// `offset` and keeping the next `keepLatest`.
func TestGetVersionsVersionOffset(t *testing.T) {
	// Five versions; GetVersions sorts them newest-first: 5,4,3,2,1.
	allVersions := []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0", "5.0.0"}
	imgs := make([]images.Image, 0, len(allVersions))
	for _, v := range allVersions {
		imgs = append(imgs, images.Image{ID: v + "-uuid", Name: imageName(v)})
	}

	tests := []struct {
		name       string
		keepLatest int
		offset     int
		want       []string
	}{
		{
			name:       "offset 0 keeps newest N",
			keepLatest: 3,
			offset:     0,
			want:       []string{"5.0.0", "4.0.0", "3.0.0"},
		},
		{
			name:       "offset skips the newest versions",
			keepLatest: 2,
			offset:     2,
			want:       []string{"3.0.0", "2.0.0"},
		},
		{
			name:       "offset reaches the oldest window",
			keepLatest: 2,
			offset:     3,
			want:       []string{"2.0.0", "1.0.0"},
		},
		{
			name:       "offset applies even when keepLatest covers all versions",
			keepLatest: 5,
			offset:     2,
			want:       []string{"3.0.0", "2.0.0", "1.0.0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newTestGlance(t, GlanceParams{
				Regions:       []string{testRegion},
				KeepLatest:    tc.keepLatest,
				VersionOffset: tc.offset,
			}, map[string][]images.Image{testRegion: imgs})

			versions, err := g.GetVersions(context.Background())
			if err != nil {
				t.Fatalf("GetVersions: %v", err)
			}
			got := versionStrings(versions)
			if !slices.Equal(got, tc.want) {
				t.Errorf("versions = %v, want %v", got, tc.want)
			}
		})
	}
}

// A VersionOffset whose window runs past the available versions must not panic
// and must not slice out of bounds.
func TestGetVersionsVersionOffsetOutOfBounds(t *testing.T) {
	allVersions := []string{"1.0.0", "2.0.0", "3.0.0", "4.0.0"}
	imgs := make([]images.Image, 0, len(allVersions))
	for _, v := range allVersions {
		imgs = append(imgs, images.Image{ID: v + "-uuid", Name: imageName(v)})
	}

	// len=4, keepLatest=3 satisfies the len>keepLatest guard, but offset=2 makes
	// the upper bound offset+keepLatest = 5, which is past len(versions).
	g := newTestGlance(t, GlanceParams{
		Regions:       []string{testRegion},
		KeepLatest:    3,
		VersionOffset: 2,
	}, map[string][]images.Image{testRegion: imgs})

	versions, err := g.GetVersions(context.Background())
	if err != nil {
		t.Fatalf("GetVersions: %v", err)
	}
	// Expect the window to be clamped to the available versions. Versions sorted
	// newest-first are 4,3,2,1; skipping the 2 newest leaves 2.0.0, 1.0.0. The
	// upper bound must clamp instead of panicking.
	got := versionStrings(versions)
	want := []string{"2.0.0", "1.0.0"}
	if !slices.Equal(got, want) {
		t.Errorf("versions = %v, want %v (window must clamp to available range)", got, want)
	}
}

// newTestGlance builds a Glance source with auth/list stubbed so no real OpenStack is contacted.
func newTestGlance(t *testing.T, params GlanceParams, imgsByRegion map[string][]images.Image) *Glance {
	t.Helper()
	if params.AuthURLFormat == "" {
		params.AuthURLFormat = "https://identity-3.%s.cloud.sap/v3"
	}
	g, err := NewGlance(params, logr.Discard())
	if err != nil {
		t.Fatalf("NewGlance: %v", err)
	}
	g.authenticate = func(ctx context.Context, authURL string, opts gophercloud.AuthOptions) (*gophercloud.ProviderClient, error) {
		return &gophercloud.ProviderClient{}, nil
	}
	g.listImages = func(ctx context.Context, provider *gophercloud.ProviderClient, region string) ([]images.Image, error) {
		return imgsByRegion[region], nil
	}
	return g
}
