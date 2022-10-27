/*
Copyright 2022.

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.


type BentoContext struct {
	BentomlVersion string `json:"bentomlVersion,omitempty"`
}

type BentoModel struct {
	// +kubebuilder:validation:Required
	Tag string `json:"tag"`
	DownloadURL string `json:"downloadURL,omitempty"`
}

type BentoRunner struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	ModelTags []string `json:"modelTags,omitempty"`
}

// BentoSpec defines the desired state of Bento
type BentoSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +kubebuilder:validation:Required
	Tag string `json:"tag"`
	DownloadURL string `json:"downloadURL,omitempty"`
	Context BentoContext `json:"context,omitempty"`
	Runners []BentoRunner `json:"runners,omitempty"`
	Models []BentoModel `json:"models,omitempty"`
}

// BentoStatus defines the observed state of Bento
type BentoStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +optional
	Image string `json:"image,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// Bento is the Schema for the bentoes API
type Bento struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BentoSpec   `json:"spec,omitempty"`
	Status BentoStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// BentoList contains a list of Bento
type BentoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bento `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Bento{}, &BentoList{})
}
