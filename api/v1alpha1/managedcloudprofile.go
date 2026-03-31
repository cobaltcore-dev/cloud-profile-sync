// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

func init() {
	SchemeBuilder.Register(&ManagedCloudProfile{}, &ManagedCloudProfileList{})
}

type ManagedCloudProfileSpec struct {
	// CloudProfile contains the base spec of the CloudProfile.
	CloudProfile CloudProfileSpec `json:"cloudProfile"`

	// MachineImageUpdates contains the source and provider information to automate machine images.
	// +optional
	MachineImageUpdates []MachineImageUpdate `json:"machineImageUpdates,omitempty"`
	// GarbageCollection contains configuration for automated garbage collection
	// +optional
	GarbageCollection *GarbageCollectionConfig `json:"garbageCollection,omitempty"`
}

// Copy the cloud profile spec to override some validation

type CloudProfileSpec struct {
	// CABundle is a certificate bundle which will be installed onto every host machine of shoot cluster targeting this profile.
	// +optional
	CABundle *string `json:"caBundle,omitempty"`
	// Kubernetes contains constraints regarding allowed values of the 'kubernetes' block in the Shoot specification.
	Kubernetes gardenerv1beta1.KubernetesSettings `json:"kubernetes"`
	// MachineImages contains constraints regarding allowed values for machine images in the Shoot specification.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	MachineImages []gardenerv1beta1.MachineImage `json:"machineImages" patchStrategy:"merge" patchMergeKey:"name"`
	// MachineTypes contains constraints regarding allowed values for machine types in the 'workers' block in the Shoot specification.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	MachineTypes []gardenerv1beta1.MachineType `json:"machineTypes" patchStrategy:"merge" patchMergeKey:"name"`
	// ProviderConfig contains provider-specific configuration for the profile.
	// +optional
	ProviderConfig *runtime.RawExtension `json:"providerConfig,omitempty"`
	// Regions contains constraints regarding allowed values for regions and zones.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	Regions []gardenerv1beta1.Region `json:"regions" patchStrategy:"merge" patchMergeKey:"name"`
	// SeedSelector contains an optional list of labels on `Seed` resources that marks those seeds whose shoots may use this provider profile.
	// An empty list means that all seeds of the same provider type are supported.
	// This is useful for environments that are of the same type (like openstack) but may have different "instances"/landscapes.
	// Optionally a list of possible providers can be added to enable cross-provider scheduling. By default, the provider
	// type of the seed must match the shoot's provider.
	// +optional
	SeedSelector *gardenerv1beta1.SeedSelector `json:"seedSelector,omitempty"`
	// Type is the name of the provider.
	Type string `json:"type"`
	// VolumeTypes contains constraints regarding allowed values for volume types in the 'workers' block in the Shoot specification.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +optional
	VolumeTypes []gardenerv1beta1.VolumeType `json:"volumeTypes,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
	// Bastion contains the machine and image properties
	// +optional
	Bastion *gardenerv1beta1.Bastion `json:"bastion,omitempty"`
	// Limits configures operational limits for Shoot clusters using this CloudProfile.
	// See https://github.com/gardener/gardener/blob/master/docs/usage/shoot/shoot_limits.md.
	// +optional
	Limits *gardenerv1beta1.Limits `json:"limits,omitempty"`
	// MachineCapabilities contains the definition of all possible capabilities in the CloudProfile.
	// Only capabilities and values defined here can be used to describe MachineImages and MachineTypes.
	// The order of values for a given capability is relevant. The most important value is listed first.
	// During maintenance upgrades, the image that matches most capabilities will be selected.
	// +optional
	MachineCapabilities []gardenerv1beta1.CapabilityDefinition `json:"machineCapabilities,omitempty"`
}

type SecretReference struct {
	// Name of a Secret.
	Name string `json:"name"`

	// Namespace of a Secret.
	Namespace string `json:"namespace"`

	// Key within the Secret to use for required data.
	Key string `json:"key"`
}

type MachineImageUpdate struct {
	// Source contains configuration for a source for machine images.
	Source MachineImageUpdateSource `json:"source"`

	// Provider contains configuration for a provider for machine images.
	Provider MachineImageUpdateProvider `json:"provider"`

	// ImagesName is the name of the image to maintain automatically
	ImageName string `json:"imageName"`
}

type GarbageCollectionConfig struct {
	// Enabled toggles garbage collection for this image.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
	// MaxAge defines the maximum age for images to keep. Images older than
	// now - MaxAge are eligible for deletion.
	// +optional
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('0s')",message="maxAge must not be negative"
	MaxAge metav1.Duration `json:"maxAge,omitempty"`
}

type MachineImageUpdateSource struct {
	// OCI contains configuration for an OCI source.
	// +optional
	OCI *MachineImageUpdateSourceOCI `json:"oci,omitempty"`
}

type MachineImageUpdateSourceOCI struct {
	// Registry contains the hostname and port of the OCI registry
	Registry string `json:"registry"`
	// Repository contains the monitored repository
	Repository string `json:"repository"`
	// Username for authentication
	// +optional
	Username string `json:"username,omitempty"`
	// Password for authentication
	// +optional
	Password SecretReference `json:"password,omitempty"`
	// Insecure disables TLS
	// +optional
	Insecure bool `json:"insecure,omitempty"`
}

type MachineImageUpdateProvider struct {
	// Ironcore contains configuration to update provider.machineImages for ironcore-metal CloudProfiles
	// +optional
	IroncoreMetal *MachineImagesUpdateProviderIroncoreMetal `json:"ironcoreMetal,omitempty"`
}

type MachineImagesUpdateProviderIroncoreMetal struct {
	// Registry contains the hostname and port of the OCI registry
	Registry string `json:"registry"`
	// Repository contains the repository containing images
	Repository string `json:"repository"`
}

type ReconcileStatus string

const (
	SucceededReconcileStatus ReconcileStatus = "Succeeded"
	FailedReconcileStatus    ReconcileStatus = "Failed"
)

type ManagedCloudProfileStatus struct {
	// Summarized status of the ManagedCloudProfile
	// +optional
	Status ReconcileStatus `json:"status"`

	// Conditions represents the latest available observations of the server's current state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=managedcloudprofiles
//+kubebuilder:resource:singular=managedcloudprofile
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.status`

// ManagedCloudProfile is the Schema for the ManagedCloudProfile API.
type ManagedCloudProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedCloudProfileSpec   `json:"spec,omitempty"`
	Status ManagedCloudProfileStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ManagedCloudProfileList contains a list of ManagedCloudProfiles.
type ManagedCloudProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedCloudProfile `json:"items"`
}
