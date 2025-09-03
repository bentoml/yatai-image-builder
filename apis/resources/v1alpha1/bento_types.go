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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type BentoRegcred struct {
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

type BentoContext struct {
	BentomlVersion string `json:"bentomlVersion,omitempty"`
}

type BentoModel struct {
	// +kubebuilder:validation:Required
	Tag         string             `json:"tag"`
	DownloadURL string             `json:"downloadUrl,omitempty"`
	Size        *resource.Quantity `json:"size,omitempty"`
}

type BentoRunner struct {
	// +kubebuilder:validation:Required
	Name         string         `json:"name"`
	RunnableType string         `json:"runnableType,omitempty"`
	ModelTags    []string       `json:"modelTags,omitempty"`
	Manifest     *BentoManifest `json:"manifest,omitempty"`
}

// BentoSpec defines the desired state of Bento
type BentoSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +kubebuilder:validation:Required
	Tag string `json:"tag"`
	// +kubebuilder:validation:Required
	Image       string         `json:"image"`
	ServiceName string         `json:"serviceName,omitempty"`
	Context     *BentoContext  `json:"context,omitempty"`
	Runners     []BentoRunner  `json:"runners,omitempty"`
	Models      []BentoModel   `json:"models,omitempty"`
	Manifest    *BentoManifest `json:"manifest,omitempty"`

	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// BentoStatus defines the observed state of Bento
type BentoStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Ready bool `json:"ready"`
}

//+genclient
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Tag",type="string",JSONPath=".spec.tag",description="Tag"
//+kubebuilder:printcolumn:name="Image",type="string",JSONPath=".spec.image",description="Image"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

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
