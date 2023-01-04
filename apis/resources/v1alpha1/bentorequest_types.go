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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	BentoRequestConditionTypeImageBuilding  = "ImageBuilding"
	BentoRequestConditionTypeImageExists    = "ImageExists"
	BentoRequestConditionTypeBentoAvailable = "BentoAvailable"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type ExtraPodMetadata struct {
	Annotations map[string]string `json:"annotations,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type ExtraPodSpec struct {
	SchedulerName             string                            `json:"schedulerName,omitempty"`
	NodeSelector              map[string]string                 `json:"nodeSelector,omitempty"`
	Affinity                  *corev1.Affinity                  `json:"affinity,omitempty"`
	Tolerations               []corev1.Toleration               `json:"tolerations,omitempty"`
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	ServiceAccountName        string                            `json:"serviceAccountName,omitempty"`
}

// BentoRequestSpec defines the desired state of BentoRequest
type BentoRequestSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// +kubebuilder:validation:Required
	BentoTag    string        `json:"bentoTag"`
	DownloadURL string        `json:"downloadUrl,omitempty"`
	Context     *BentoContext `json:"context,omitempty"`
	Runners     []BentoRunner `json:"runners,omitempty"`
	Models      []BentoModel  `json:"models,omitempty"`

	ImageBuildTimeout *time.Duration `json:"imageBuildTimeout,omitempty"`

	// +kubebuilder:validation:Optional
	ImageBuilderExtraPodMetadata *ExtraPodMetadata `json:"imageBuilderExtraPodMetadata,omitempty"`
	// +kubebuilder:validation:Optional
	ImageBuilderExtraPodSpec *ExtraPodSpec `json:"imageBuilderExtraPodSpec,omitempty"`
	// +kubebuilder:validation:Optional
	ImageBuilderExtraContainerEnv []corev1.EnvVar `json:"imageBuilderExtraContainerEnv,omitempty"`
	// +kubebuilder:validation:Optional
	ImageBuilderContainerResources *corev1.ResourceRequirements `json:"imageBuilderContainerResources,omitempty"`

	// +kubebuilder:validation:Optional
	DockerConfigJSONSecretName string `json:"dockerConfigJsonSecretName,omitempty"`

	// +kubebuilder:validation:Optional
	DownloaderContainerEnvFrom []corev1.EnvFromSource `json:"downloaderContainerEnvFrom,omitempty"`
}

// BentoRequestStatus defines the observed state of BentoRequest
type BentoRequestStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Conditions            []metav1.Condition `json:"conditions"`
	ImageBuilderPodStatus corev1.PodStatus   `json:"imageBuilderPodStatus"`
}

//+genclient
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Bento-Tag",type="string",JSONPath=".spec.bentoTag",description="Bento Tag"
//+kubebuilder:printcolumn:name="Download-Url",type="string",JSONPath=".spec.downloadUrl",description="Download URL"
//+kubebuilder:printcolumn:name="Image-Exists",type="string",JSONPath=".status.conditions[?(@.type=='ImageExists')].status",description="Image Exists"
//+kubebuilder:printcolumn:name="Bento-Available",type="string",JSONPath=".status.conditions[?(@.type=='BentoAvailable')].status",description="Bento Available"
//+kubebuilder:printcolumn:name="Image-Builder-Pod-Phase",type="string",JSONPath=".status.imageBuilderPodStatus.phase",description="Image Builder Pod Phase"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// BentoRequest is the Schema for the bentorequests API
type BentoRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BentoRequestSpec   `json:"spec,omitempty"`
	Status BentoRequestStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// BentoRequestList contains a list of BentoRequest
type BentoRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BentoRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BentoRequest{}, &BentoRequestList{})
}
