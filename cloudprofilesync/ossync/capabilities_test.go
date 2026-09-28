// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ossync

import (
	"testing"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
)

func TestCapabilitiesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b gardenerv1beta1.Capabilities
		want bool
	}{
		{
			name: "both nil",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "both empty",
			a:    gardenerv1beta1.Capabilities{},
			b:    gardenerv1beta1.Capabilities{},
			want: true,
		},
		{
			name: "nil and empty map are equal (both have length 0)",
			a:    nil,
			b:    gardenerv1beta1.Capabilities{},
			want: true,
		},
		{
			name: "equal: single key same values",
			a:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}},
			b:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}},
			want: true,
		},
		{
			name: "equal: multiple keys same values",
			a:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "usi"}},
			b:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci", "usi"}},
			want: true,
		},
		{
			name: "different value for a key",
			a:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}},
			b:    gardenerv1beta1.Capabilities{"architecture": {"arm64"}},
			want: false,
		},
		{
			name: "key present in a but missing in b",
			a:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci"}},
			b:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}},
			want: false,
		},
		{
			name: "key present in b but missing in a",
			a:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}},
			b:    gardenerv1beta1.Capabilities{"architecture": {"amd64"}, "feature": {"sci"}},
			want: false,
		},
		{
			// slices.Equal is order-sensitive: value order within a key is significant.
			name: "same values different order within key",
			a:    gardenerv1beta1.Capabilities{"feature": {"sci", "usi"}},
			b:    gardenerv1beta1.Capabilities{"feature": {"usi", "sci"}},
			want: false,
		},
		{
			name: "same key different number of values",
			a:    gardenerv1beta1.Capabilities{"feature": {"sci", "usi"}},
			b:    gardenerv1beta1.Capabilities{"feature": {"sci"}},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapabilitiesEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("CapabilitiesEqual(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// Symmetry: CapabilitiesEqual must return the same result regardless of argument order.
			if got := CapabilitiesEqual(tc.b, tc.a); got != tc.want {
				t.Errorf("CapabilitiesEqual(%v, %v) [swapped] = %v, want %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

func TestMergeCapabilityFlavor(t *testing.T) {
	caps1 := gardenerv1beta1.Capabilities{"architecture": {"amd64"}}
	caps2 := gardenerv1beta1.Capabilities{"architecture": {"arm64"}}

	t.Run("append to nil slice", func(t *testing.T) {
		result := mergeCapabilityFlavor(nil, caps1)
		if len(result) != 1 {
			t.Fatalf("got %d flavors, want 1", len(result))
		}
		if !CapabilitiesEqual(result[0].Capabilities, caps1) {
			t.Errorf("flavor capabilities = %v, want %v", result[0].Capabilities, caps1)
		}
	})

	t.Run("does not duplicate an existing flavor", func(t *testing.T) {
		existing := []gardenerv1beta1.MachineImageFlavor{
			{Capabilities: caps1},
		}
		result := mergeCapabilityFlavor(existing, caps1)
		if len(result) != 1 {
			t.Errorf("got %d flavors after merge of existing caps, want 1 (must not duplicate)", len(result))
		}
	})

	t.Run("appends a new flavor when capabilities differ", func(t *testing.T) {
		existing := []gardenerv1beta1.MachineImageFlavor{
			{Capabilities: caps1},
		}
		result := mergeCapabilityFlavor(existing, caps2)
		if len(result) != 2 {
			t.Fatalf("got %d flavors, want 2 (different caps must produce a new flavor)", len(result))
		}
	})

	t.Run("empty caps does not append", func(t *testing.T) {
		result := mergeCapabilityFlavor(nil, gardenerv1beta1.Capabilities{})
		if len(result) != 0 {
			t.Errorf("got %d flavors for empty caps, want 0", len(result))
		}
	})

	t.Run("nil caps does not append", func(t *testing.T) {
		result := mergeCapabilityFlavor(nil, nil)
		if len(result) != 0 {
			t.Errorf("got %d flavors for nil caps, want 0", len(result))
		}
	})
}
