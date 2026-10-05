// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ossync

import (
	"slices"

	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
)

// ArchitectureCapability is the well-known Gardener capability key for CPU architecture.
// It is read from the "architecture" OCI annotation and excluded from the user-configured
// capabilityKeys since it is always populated automatically by the OCI source.
const ArchitectureCapability = "architecture"

// FeatureSetAnnotation is the gardenlinux OCI annotation key that carries the image's
// feature set as a comma-separated list (e.g. "sci,_usi,vhost").
const FeatureSetAnnotation = "feature_set"

// CapabilitiesEqual reports whether two Capabilities maps are identical:
// same keys, same values in the same order. Value order is significant because
// Gardener treats capability value slices as ordered lists.
func CapabilitiesEqual(a, b gardenerv1beta1.Capabilities) bool {
	if len(a) != len(b) {
		return false
	}
	for k, aVals := range a {
		bVals, ok := b[k]
		if !ok {
			return false
		}
		if !slices.Equal(aVals, bVals) {
			return false
		}
	}
	return true
}

// mergeCapabilityFlavor appends a new flavor to existing if no flavor with
// equal capabilities is already present. Returns existing unchanged otherwise.
func mergeCapabilityFlavor(existing []gardenerv1beta1.MachineImageFlavor, caps gardenerv1beta1.Capabilities) []gardenerv1beta1.MachineImageFlavor {
	if len(caps) == 0 {
		return existing
	}
	for _, f := range existing {
		if CapabilitiesEqual(f.Capabilities, caps) {
			return existing
		}
	}
	return append(existing, gardenerv1beta1.MachineImageFlavor{Capabilities: caps})
}

// allowedCapabilityValues builds a per-key set of allowed values from the
// MachineCapabilities declared in the CloudProfile spec. Only values present
// here will be written into capabilityFlavors.
func allowedCapabilityValues(caps []gardenerv1beta1.CapabilityDefinition) map[string]map[string]struct{} {
	allowed := make(map[string]map[string]struct{}, len(caps))
	for _, cap := range caps {
		vals := make(map[string]struct{}, len(cap.Values))
		for _, v := range cap.Values {
			vals[v] = struct{}{}
		}
		allowed[cap.Name] = vals
	}
	return allowed
}

// filterCapabilities returns a copy of caps filtered to only the key/value pairs
// declared in allowed. Keys or values absent from allowed are dropped.
// Returns nil if allowed is empty — MachineCapabilities not configured means
// no capabilities should be written to the CloudProfile.
func filterCapabilities(caps gardenerv1beta1.Capabilities, allowed map[string]map[string]struct{}) gardenerv1beta1.Capabilities {
	if len(allowed) == 0 {
		return nil
	}
	result := make(gardenerv1beta1.Capabilities, len(caps))
	for key, values := range caps {
		allowedVals, ok := allowed[key]
		if !ok {
			continue
		}
		var filtered []string
		for _, v := range values {
			if _, ok := allowedVals[v]; ok {
				filtered = append(filtered, v)
			}
		}
		if len(filtered) > 0 {
			result[key] = filtered
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
