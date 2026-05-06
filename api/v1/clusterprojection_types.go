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

// ClusterProjectionSpec is the desired state of a cluster-scoped Projection.
//
// A ClusterProjection fans out one source object into many namespaces
// selected by either an explicit list or a label selector. Cluster-tier
// authority is required to create one — the chart does NOT aggregate the
// `clusterprojections` CRUD verbs into the built-in `admin`/`edit` roles.
type ClusterProjectionSpec struct {
	// Source identifies the object to project.
	Source SourceRef `json:"source"`

	// Destination configures the fan-out target set and optional rename.
	Destination ClusterProjectionDestination `json:"destination"`

	// Overlay applies metadata patches uniformly to every projected copy.
	// (Per-target overlays are not supported in v0.3.)
	// +optional
	Overlay Overlay `json:"overlay,omitempty"`
}

// ClusterProjectionDestination is the cluster-scoped destination spec.
//
// `namespaces` and `namespaceSelector` are mutually exclusive (CEL admission)
// and at least one must be set.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.namespaces) && has(self.namespaceSelector))",message="namespaces and namespaceSelector are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="has(self.namespaces) || has(self.namespaceSelector)",message="one of namespaces or namespaceSelector must be set"
type ClusterProjectionDestination struct {
	// Namespaces is an explicit list of destination namespaces. Mutually
	// exclusive with NamespaceSelector. Each entry is a DNS-1123 label;
	// at least one entry is required when this field is set.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	Namespaces []string `json:"namespaces,omitempty"`

	// NamespaceSelector picks destination namespaces by label. Mutually
	// exclusive with Namespaces.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// Name in each destination namespace (DNS-1123 subdomain). Defaults to
	// Source.Name when empty. The same Name is written into every targeted
	// namespace.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
}

// ClusterProjectionStatus reports the rollup of the most recent reconcile.
type ClusterProjectionStatus struct {
	// Conditions: SourceResolved, DestinationWritten, Ready.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DestinationName is the resolved name of the destination object,
	// identical across all targeted namespaces. Populated after the first
	// successful write; empty before that.
	// +optional
	DestinationName string `json:"destinationName,omitempty"`

	// NamespacesWritten is the count of namespaces in which the destination
	// was successfully created or updated during the most recent reconcile.
	// +optional
	NamespacesWritten int32 `json:"namespacesWritten,omitempty"`

	// NamespacesFailed is the count of namespaces where the write failed
	// during the most recent reconcile. The DestinationWritten condition's
	// message carries a (truncated) list of failed namespace names.
	// +optional
	NamespacesFailed int32 `json:"namespacesFailed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cproj
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.source.kind`
// +kubebuilder:printcolumn:name="Source-Group",type=string,JSONPath=`.spec.source.group`,priority=1
// +kubebuilder:printcolumn:name="Source-Namespace",type=string,JSONPath=`.spec.source.namespace`
// +kubebuilder:printcolumn:name="Source-Name",type=string,JSONPath=`.spec.source.name`
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=`.status.destinationName`
// +kubebuilder:printcolumn:name="Targets",type=integer,JSONPath=`.status.namespacesWritten`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.namespacesFailed`,priority=1
// +kubebuilder:printcolumn:name="Selector",type=string,JSONPath=`.spec.destination.namespaceSelector.matchLabels`,priority=1
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterProjection mirrors one source object into many destination namespaces.
type ClusterProjection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterProjectionSpec   `json:"spec,omitempty"`
	Status ClusterProjectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterProjectionList contains a list of ClusterProjection.
type ClusterProjectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProjection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterProjection{}, &ClusterProjectionList{})
}
