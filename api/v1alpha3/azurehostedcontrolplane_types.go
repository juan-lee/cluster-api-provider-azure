/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha3

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/errors"
)

// AzureHostedControlPlaneSpec defines the desired state of AzureHostedControlPlane
type AzureHostedControlPlaneSpec struct {
	// ProviderID is the unique identifier as specified by the cloud provider.
	// +optional
	ProviderID *string `json:"providerID,omitempty"`

	// Image is used to provide details of an image to use during VM creation.
	// If image details are omitted the image will default the Azure Marketplace "capi" offer,
	// which is based on Ubuntu.
	// +kubebuilder:validation:nullable
	// +optional
	Image *Image `json:"image,omitempty"`

	Location string `json:"location"`

	SSHPublicKey string `json:"sshPublicKey"`

	// AdditionalTags is an optional set of tags to add to an instance, in addition to the ones added by default by the
	// Azure provider. If both the AzureCluster and the AzureMachine specify the same tag name with different values, the
	// AzureMachine's value takes precedence.
	// +optional
	AdditionalTags Tags `json:"additionalTags,omitempty"`
}

// AzureHostedControlPlaneStatus defines the observed state of AzureHostedControlPlane
type AzureHostedControlPlaneStatus struct {
	// Ready is true when the provider resource is ready.
	// +optional
	Ready bool `json:"ready"`

	// Addresses contains the Azure instance associated addresses.
	Addresses []v1.NodeAddress `json:"addresses,omitempty"`

	// VMState is the provisioning state of the Azure virtual machine.
	// +optional
	VMState *VMState `json:"vmState,omitempty"`

	// ErrorReason will be set in the event that there is a terminal problem
	// reconciling the Machine and will contain a succinct value suitable
	// for machine interpretation.
	//
	// This field should not be set for transitive errors that a controller
	// faces that are expected to be fixed automatically over
	// time (like service outages), but instead indicate that something is
	// fundamentally wrong with the Machine's spec or the configuration of
	// the controller, and that manual intervention is required. Examples
	// of terminal errors would be invalid combinations of settings in the
	// spec, values that are unsupported by the controller, or the
	// responsible controller itself being critically misconfigured.
	//
	// Any transient errors that occur during the reconciliation of Machines
	// can be added as events to the Machine object and/or logged in the
	// controller's output.
	// +optional
	FailureReason *errors.MachineStatusError `json:"failureReason,omitempty"`

	// ErrorMessage will be set in the event that there is a terminal problem
	// reconciling the Machine and will contain a more verbose string suitable
	// for logging and human consumption.
	//
	// This field should not be set for transitive errors that a controller
	// faces that are expected to be fixed automatically over
	// time (like service outages), but instead indicate that something is
	// fundamentally wrong with the Machine's spec or the configuration of
	// the controller, and that manual intervention is required. Examples
	// of terminal errors would be invalid combinations of settings in the
	// spec, values that are unsupported by the controller, or the
	// responsible controller itself being critically misconfigured.
	//
	// Any transient errors that occur during the reconciliation of Machines
	// can be added as events to the Machine object and/or logged in the
	// controller's output.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=azurehostedcontrolplanes,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:subresource:status

// AzureHostedControlPlane is the Schema for the azurehostedcontrolplanes API
type AzureHostedControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AzureHostedControlPlaneSpec   `json:"spec,omitempty"`
	Status AzureHostedControlPlaneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AzureHostedControlPlaneList contains a list of AzureHostedControlPlane
type AzureHostedControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AzureHostedControlPlane `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AzureHostedControlPlane{}, &AzureHostedControlPlaneList{})
}
