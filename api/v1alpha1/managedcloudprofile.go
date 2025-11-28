// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	gardenerv1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&ManagedCloudProfile{}, &ManagedCloudProfileList{})
}

type ManagedCloudProfileSpec struct {
	// CloudProfile contains the base spec of the CloudProfile.
	CloudProfile gardenerv1beta1.CloudProfileSpec `json:"cloudProfile"`

	// MachineImages contains the source and provider information to automate machine images.
	// +optional
	MachineImages *ManagedCloudProfileMachineImages `json:"machineImages,omitempty"`
}

type SecretReference struct {
	// Name of a Secret.
	Name string `json:"name"`

	// Namespace of a Secret.
	Namespace string `json:"namespace"`

	// Key within the Secret to use for required data.
	Key string `json:"key"`
}

type ManagedCloudProfileMachineImages struct {
	// Source contains configuration for a source for machine images.
	Source ManagedCloudProfileMachineImagesSource `json:"source"`

	// Provider contains configuration for a provider for machine images.
	Provider ManagedCloudProfileMachineImagesProvider `json:"provider"`

	// ImagesName is the name of the image to maintain automatically
	ImageName string `json:"imageName"`
}

type ManagedCloudProfileMachineImagesSource struct {
	// OCI contains configuration for an OCI source.
	OCI *ManagedCloudProfileMachineImagesSourceOCI `json:"oci,omitempty"`
}

type ManagedCloudProfileMachineImagesSourceOCI struct {
	Registry   string          `json:"registry"`
	Repository string          `json:"repository"`
	Username   string          `json:"username"`
	Password   SecretReference `json:"password"`
	Insecure   bool            `json:"insecure"`
}

type ManagedCloudProfileMachineImagesProvider struct {
	// Ironcore contains configuration to update provider.machineImages for ironcore-metal CloudProfiles
	IroncoreMetal *ManagedCloudProfileMachineImagesProviderIroncoreMetal `json:"ironcoreMetal,omitempty"`
}

type ManagedCloudProfileMachineImagesProviderIroncoreMetal struct {
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
}

type ManagedCloudProfileStatus struct{}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=managedcloudprofiles
//+kubebuilder:resource:singular=managedcloudprofile
//+kubebuilder:resource:scope=Cluster

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
