// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cobaltcore-dev/cloud-profile-sync/api/v1alpha1"
)

var _ = Describe("splitAnnotationRaw", func() {
	It("returns empty map for empty string", func() {
		Expect(splitAnnotationRaw("")).To(BeEmpty())
	})

	It("returns empty map for whitespace-only string", func() {
		Expect(splitAnnotationRaw("  ,  ,  ")).To(BeEmpty())
	})

	It("preserves tokens including leading underscores", func() {
		result := splitAnnotationRaw("scibase,_usi,vhost")
		Expect(result).To(HaveKey("scibase"))
		Expect(result).To(HaveKey("_usi"))
		Expect(result).To(HaveKey("vhost"))
		Expect(result).To(HaveLen(3))
	})

	It("trims whitespace around tokens", func() {
		result := splitAnnotationRaw(" scibase , _usi ")
		Expect(result).To(HaveKey("scibase"))
		Expect(result).To(HaveKey("_usi"))
	})

	It("deduplicates repeated tokens", func() {
		result := splitAnnotationRaw("sci,sci,_usi")
		Expect(result).To(HaveLen(2))
		Expect(result).To(HaveKey("sci"))
		Expect(result).To(HaveKey("_usi"))
	})
})

var _ = Describe("supportsInPlaceUpdate", func() {
	It("returns false when feature_set annotation is absent", func() {
		Expect(supportsInPlaceUpdate(map[string]string{"architecture": "amd64"})).To(BeFalse())
	})

	It("returns false when feature_set annotation is present but empty", func() {
		Expect(supportsInPlaceUpdate(map[string]string{"architecture": "amd64", "feature_set": ""})).To(BeFalse())
	})

	It("returns false when _usi is absent from feature_set", func() {
		Expect(supportsInPlaceUpdate(map[string]string{"feature_set": "scibase,vhost"})).To(BeFalse())
	})

	It("returns false when only the normalized form 'usi' is present (not '_usi')", func() {
		Expect(supportsInPlaceUpdate(map[string]string{"feature_set": "scibase,usi"})).To(BeFalse())
	})

	It("returns true when _usi is present in feature_set", func() {
		Expect(supportsInPlaceUpdate(map[string]string{"feature_set": "scibase,_usi,vhost"})).To(BeTrue())
	})
})

var _ = Describe("passesImageFilter", func() {
	It("passes when filter is nil", func() {
		Expect(passesImageFilter(map[string]struct{}{"sci": {}}, nil)).To(BeTrue())
	})

	It("passes when RequiredFeatureSetValues is empty", func() {
		filter := &v1alpha1.ImageFilter{}
		Expect(passesImageFilter(map[string]struct{}{"sci": {}}, filter)).To(BeTrue())
	})

	It("passes when all required values are present", func() {
		filter := &v1alpha1.ImageFilter{RequiredFeatureSetValues: []string{"scibase", "_usi"}}
		tokens := map[string]struct{}{"scibase": {}, "_usi": {}, "vhost": {}}
		Expect(passesImageFilter(tokens, filter)).To(BeTrue())
	})

	It("fails when a required value is absent", func() {
		filter := &v1alpha1.ImageFilter{RequiredFeatureSetValues: []string{"scibase", "_usi"}}
		tokens := map[string]struct{}{"scibase": {}}
		Expect(passesImageFilter(tokens, filter)).To(BeFalse())
	})

	It("fails when normalized form is present but raw form is required", func() {
		filter := &v1alpha1.ImageFilter{RequiredFeatureSetValues: []string{"_usi"}}
		tokens := map[string]struct{}{"usi": {}}
		Expect(passesImageFilter(tokens, filter)).To(BeFalse())
	})

	It("fails when token set is empty", func() {
		filter := &v1alpha1.ImageFilter{RequiredFeatureSetValues: []string{"scibase"}}
		Expect(passesImageFilter(map[string]struct{}{}, filter)).To(BeFalse())
	})
})
