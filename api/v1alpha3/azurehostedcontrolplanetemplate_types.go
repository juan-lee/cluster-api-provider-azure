/*
Copyright 2019 The Kubernetes Authors.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AzureHostedControlPlaneTemplateSpec defines the desired state of AzureHostedControlPlaneTemplate
type AzureHostedControlPlaneTemplateSpec struct {
	Template AzureHostedControlPlaneTemplateResource `json:"template"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=azurehostedcontrolplanetemplates,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion

// AzureHostedControlPlaneTemplate is the Schema for the azurehostedcontrolplanetemplates API
type AzureHostedControlPlaneTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AzureHostedControlPlaneTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// AzureHostedControlPlaneTemplateList contains a list of AzureHostedControlPlaneTemplate
type AzureHostedControlPlaneTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AzureHostedControlPlaneTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AzureHostedControlPlaneTemplate{}, &AzureHostedControlPlaneTemplateList{})
}

// AzureHostedControlPlaneTemplateResource describes the data needed to create an AzureHostedControlPlane from a template
type AzureHostedControlPlaneTemplateResource struct {
	// Spec is the specification of the desired behavior of the machine.
	Spec AzureHostedControlPlaneSpec `json:"spec"`
}
