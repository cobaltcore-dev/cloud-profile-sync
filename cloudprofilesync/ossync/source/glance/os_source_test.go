// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package glance

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
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
