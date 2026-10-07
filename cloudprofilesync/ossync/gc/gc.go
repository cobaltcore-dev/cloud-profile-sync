// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package gc

import (
	"time"

	"github.com/cobaltcore-dev/cloud-profile-sync/cloudprofilesync/ossync"
)

type Filter struct {
	// Tags maps SourceImage.Version to the time the tag was pushed to the
	// registry. Keys must be in SourceImage.Version space (normalized).
	Tags map[string]time.Time
	// Protected is the set of versions (full tags) and clean versions that are
	// in use (e.g. referenced by a Shoot) and must never be collected. Keys must
	// be in SourceImage.Version / CleanVersion space (normalized).
	Protected map[string]struct{}
	// Cutoff is the age boundary: images pushed before Cutoff are candidates for
	// collection.
	Cutoff time.Time
}

// Filter returns the subset of images that should remain. It never mutates its
// input and preserves order.
func (f *Filter) Filter(images []ossync.SourceImage) []ossync.SourceImage {
	kept := make([]ossync.SourceImage, 0, len(images))
	for _, img := range images {
		if f.keep(img) {
			kept = append(kept, img)
		}
	}
	return kept
}

func (f *Filter) keep(img ossync.SourceImage) bool {
	// Referenced versions are always kept, by full tag or by clean version.
	if _, ok := f.Protected[img.Version]; ok {
		return true
	}
	if img.CleanVersion != "" {
		if _, ok := f.Protected[img.CleanVersion]; ok {
			return true
		}
	}
	// Unknown push time → keep (cannot judge age).
	pushedAt, ok := f.Tags[img.Version]
	if !ok {
		return true
	}
	// Keep unless strictly older than the cutoff.
	return !pushedAt.Before(f.Cutoff)
}
