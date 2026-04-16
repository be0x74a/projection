/*
Copyright 2024.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SourceRef identifies the Kubernetes object to project from.
type SourceRef struct {
	// APIVersion of the source object, e.g. "v1" or "apps/v1".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^([a-z0-9.-]+/)?v[0-9]+((alpha|beta)[0-9]+)?$`
	APIVersion string `json:"apiVersion"`
	// Kind of the source object, e.g. "ConfigMap".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[A-Z][A-Za-z0-9]*$`
	Kind string `json:"kind"`
	// Name of the source object.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// Namespace of the source object.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`
}

// DestinationRef identifies where the projected object should be written.
// Invariant: namespace and namespaceSelector are mutually exclusive. Enforced
// by the reconciler rather than a CEL rule because older apiserver versions
// (k8s 1.31 and earlier) can't resolve `self.namespace` in x-kubernetes-validations
// for plain-string fields with omitempty, producing "undefined field" errors at
// CRD install time.
type DestinationRef struct {
	// Namespace to project into. Defaults to the Projection's own namespace.
	// Mutually exclusive with NamespaceSelector.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`
	// NamespaceSelector selects namespaces to project into by label.
	// Mutually exclusive with Namespace.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	// Name in the destination namespace. Defaults to Source.Name.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name,omitempty"`
}

// Overlay is applied on top of the source object's metadata before writing
// to the destination. Overlay entries win on key conflicts with the source.
type Overlay struct {
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ProjectionSpec defines the desired state of Projection.
type ProjectionSpec struct {
	// Source is the object to project from.
	Source SourceRef `json:"source"`
	// Destination controls where the projected object is written.
	// +optional
	Destination DestinationRef `json:"destination,omitempty"`
	// Overlay applies metadata patches on top of the projected object.
	// +optional
	Overlay Overlay `json:"overlay,omitempty"`
}

// ProjectionStatus defines the observed state of Projection.
type ProjectionStatus struct {
	// Conditions reflect the current state of the projection. The controller
	// sets type "Ready" to True once the destination has been written, or
	// False with a reason describing why not.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.source.kind`
// +kubebuilder:printcolumn:name="Source-Namespace",type=string,JSONPath=`.spec.source.namespace`
// +kubebuilder:printcolumn:name="Source-Name",type=string,JSONPath=`.spec.source.name`
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=`.spec.destination.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Projection is the Schema for the projections API.
type Projection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProjectionSpec   `json:"spec,omitempty"`
	Status ProjectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ProjectionList contains a list of Projection.
type ProjectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Projection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Projection{}, &ProjectionList{})
}
