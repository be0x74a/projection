/*
Copyright 2026 The projection Authors.

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

// ProjectionSpec is the desired state of a namespaced Projection.
//
// A Projection mirrors one source object into the Projection's own namespace.
// It cannot write outside its own namespace — that is what ClusterProjection
// is for. This narrowness is deliberate: it makes namespace-scoped RBAC on
// `projections.projection.sh` a structural confinement, not a policy hint.
type ProjectionSpec struct {
	// Source identifies the object to project.
	Source SourceRef `json:"source"`

	// Destination optionally renames the destination. The destination
	// namespace is implicitly the Projection's own namespace and cannot
	// be set; for cross-namespace mirroring use ClusterProjection.
	// +optional
	Destination ProjectionDestination `json:"destination,omitempty"`

	// Overlay applies metadata patches on top of the projected object.
	// +optional
	Overlay Overlay `json:"overlay,omitempty"`
}

// ProjectionDestination configures the rename override for a namespaced Projection.
type ProjectionDestination struct {
	// Name in the destination namespace. Defaults to Source.Name when empty.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
}

// ProjectionStatus reports the most recent reconcile outcome.
type ProjectionStatus struct {
	// Conditions: SourceResolved, DestinationWritten, Ready.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DestinationName is the resolved name of the destination object after
	// applying any rename override (`spec.destination.name`) or defaulting to
	// `spec.source.name`. Populated by the controller after a successful
	// write; empty before the first reconcile completes.
	// +optional
	DestinationName string `json:"destinationName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=proj
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.source.kind`
// +kubebuilder:printcolumn:name="Source-Group",type=string,JSONPath=`.spec.source.group`,priority=1
// +kubebuilder:printcolumn:name="Source-Namespace",type=string,JSONPath=`.spec.source.namespace`
// +kubebuilder:printcolumn:name="Source-Name",type=string,JSONPath=`.spec.source.name`
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=`.status.destinationName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Projection mirrors one source object into the Projection's own namespace.
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
