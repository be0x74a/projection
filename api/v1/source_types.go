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

// SourceRef identifies the object to project.
//
// Group + Version + Kind name the GVK; Namespace + Name name the object.
//
// `version` may be omitted for non-core groups, in which case the operator
// resolves the preferred served version via the RESTMapper on every
// reconcile. The core group has only `v1` as a stable form, so `version`
// MUST be set when `group` is empty — enforced by the CEL rule below.
//
// +kubebuilder:validation:XValidation:rule="self.group != '' || self.version != ''",message="version is required when group is empty (no unpinned form for core)"
type SourceRef struct {
	// Group is the API group of the source object. Empty string means the
	// core group (e.g. ConfigMap, Secret, Service).
	// +optional
	// +kubebuilder:validation:Pattern=`^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Group string `json:"group,omitempty"`

	// Version is the API version of the source object within its Group. Omit
	// for non-core groups to use the RESTMapper's preferred served version
	// (the source automatically follows CRD version promotions).
	// +optional
	// +kubebuilder:validation:Pattern=`^$|^v[0-9]+([a-z]+[0-9]+)?$`
	Version string `json:"version,omitempty"`

	// Kind is the API Kind of the source object (PascalCase).
	// +kubebuilder:validation:Pattern=`^[A-Z][a-zA-Z0-9]*$`
	Kind string `json:"kind"`

	// Namespace where the source object lives.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`

	// Name of the source object.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// Overlay applies metadata patches to the projected object on top of what the
// source carries. Only labels and annotations are mergeable — name and
// namespace cannot be touched (they are set by the controller from the
// destination spec).
type Overlay struct {
	// Labels merged onto the destination's metadata.labels. Source labels
	// win on conflict for keys the source already has; overlay wins for
	// overlay-only keys. (See concepts.md for the full merge rule.)
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Annotations merged onto the destination's metadata.annotations.
	// Same merge rule as Labels.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}
