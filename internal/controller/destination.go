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

package controller

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// Ownership stamps applied to every destination by buildDestination.
// destination-side cleanup paths (cleanup.go) read these back to identify
// objects we own. The "-projection" suffix in both keys reserves namespace
// for the cluster-scoped reconciler to use a parallel pair of keys
// (e.g. owned-by-cluster-projection / owned-by-cluster-projection-uid)
// without confusing the two ownership tiers.
const (
	// ownedByAnnotation records the owning Projection's namespaced name. The
	// label below is sufficient for cooperative lookups; the annotation is
	// the belt-and-braces ownership check that protects against a buggy or
	// malicious actor copying our label onto a stranger's object.
	ownedByAnnotation = "projection.sh/owned-by-projection"

	// ownedByUIDLabel is a label stamped on every destination by
	// buildDestination. Value is the owning Projection's UID. Enables
	// destination-side watches to filter events down to objects this
	// reconciler owns via a single cluster-wide List(LabelSelector)
	// instead of walking every namespace (#33). Label values are capped
	// at 63 chars and permit [a-z0-9-] plus dashes; Kubernetes UIDs are
	// RFC-4122 UUIDs (36 chars), both within the label-value regex and
	// well under the length limit.
	ownedByUIDLabel = "projection.sh/owned-by-projection-uid"
)

// Dropped from metadata on every projection. These are either server-owned
// (the apiserver rejects or ignores writes) or meaningful only in the source
// namespace — owner refs don't cross namespaces, and finalizers belong to
// controllers watching the source that don't know the destination exists.
var droppedMetadataFields = []string{
	"resourceVersion", "uid", "generation",
	"creationTimestamp", "deletionTimestamp", "deletionGracePeriodSeconds",
	"managedFields", "selfLink", "generateName",
	"ownerReferences", "finalizers",
}

// Dropped from annotations. last-applied-configuration causes three-way merge
// bugs if carried: a later `kubectl apply` on the destination would diff
// against the source's last-applied state.
var droppedAnnotations = []string{
	"kubectl.kubernetes.io/last-applied-configuration",
}

// Dropped spec-level fields per GVK. These are fields the apiserver allocates
// on create (clusterIP for Service, volumeName for PVC once bound, nodeName
// for Pod once scheduled) — carrying them to the destination fails the create
// because they're immutable after the fact and can't be user-supplied on a
// fresh object. Only add an entry for a Kind with a known
// "apiserver-allocated on create" field; the cost of stripping something a
// user actually wants to carry is worse than the cost of a missing entry.
var droppedSpecFieldsByGVK = map[schema.GroupVersionKind][][]string{
	{Group: "", Version: "v1", Kind: "Service"}: {
		{"spec", "clusterIP"},
		{"spec", "clusterIPs"},
		{"spec", "ipFamilies"},
		{"spec", "ipFamilyPolicy"},
	},
	{Group: "", Version: "v1", Kind: "PersistentVolumeClaim"}: {
		{"spec", "volumeName"},
	},
	{Group: "", Version: "v1", Kind: "Pod"}: {
		{"spec", "nodeName"},
	},
	{Group: "batch", Version: "v1", Kind: "Job"}: {
		{"spec", "template", "metadata", "labels", "batch.kubernetes.io/controller-uid"},
		{"spec", "template", "metadata", "labels", "batch.kubernetes.io/job-name"},
		{"spec", "template", "metadata", "labels", "controller-uid"},
		{"spec", "selector"},
	},
}

// destinationCoords resolves the namespace+name a namespaced Projection
// writes to. The destination namespace is always the Projection's own
// namespace (cross-namespace mirroring is the cluster-scoped sibling's
// job). The destination name defaults to the source's when the spec
// leaves it empty.
func destinationCoords(proj *projectionv1.Projection) (namespace, name string) {
	namespace = proj.Namespace
	name = proj.Spec.Destination.Name
	if name == "" {
		name = proj.Spec.Source.Name
	}
	return
}

// ownerKey returns the namespaced ownership identifier stamped on each
// destination via ownedByAnnotation.
func ownerKey(proj *projectionv1.Projection) string {
	return proj.Namespace + "/" + proj.Name
}

// ownedByUIDSelector returns the label selector that matches every destination
// owned by proj. The label value is the Projection's UID rather than its name
// so a delete-recreate cycle can't shadow stale destinations of a prior
// incarnation.
func ownedByUIDSelector(proj *projectionv1.Projection) string {
	return fmt.Sprintf("%s=%s", ownedByUIDLabel, proj.UID)
}

func isOwnedBy(obj *unstructured.Unstructured, proj *projectionv1.Projection) bool {
	return obj.GetAnnotations()[ownedByAnnotation] == ownerKey(proj)
}

// buildDestination builds the object to write to the given target namespace.
// It preserves the source's spec/data and user-set labels/annotations, drops
// server-owned and cross-namespace-unsafe metadata, and applies the overlay
// on top. The ownership annotation is required: subsequent reconciles use it
// to distinguish our destination from a stranger's and refuse to overwrite
// the latter.
func buildDestination(source *unstructured.Unstructured, proj *projectionv1.Projection, targetNamespace string) *unstructured.Unstructured {
	dst := source.DeepCopy()

	if metadata, ok := dst.Object["metadata"].(map[string]interface{}); ok {
		for _, f := range droppedMetadataFields {
			delete(metadata, f)
		}
	}
	for _, path := range droppedSpecFieldsByGVK[source.GroupVersionKind()] {
		unstructured.RemoveNestedField(dst.Object, path...)
	}
	delete(dst.Object, "status")

	annotations := dst.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	for _, a := range droppedAnnotations {
		delete(annotations, a)
	}
	for k, v := range proj.Spec.Overlay.Annotations {
		annotations[k] = v
	}
	annotations[ownedByAnnotation] = ownerKey(proj)
	dst.SetAnnotations(annotations)

	lbls := dst.GetLabels()
	if lbls == nil {
		lbls = map[string]string{}
	}
	for k, v := range proj.Spec.Overlay.Labels {
		lbls[k] = v
	}
	lbls[ownedByUIDLabel] = string(proj.UID)
	dst.SetLabels(lbls)

	name := proj.Spec.Destination.Name
	if name == "" {
		name = proj.Spec.Source.Name
	}
	dst.SetNamespace(targetNamespace)
	dst.SetName(name)

	return dst
}

// preserveAPIServerAllocatedFields copies fields the apiserver assigns on
// create (clusterIP, volumeName, nodeName, ...) from the existing destination
// onto the desired object. Without this, an Update of e.g. a Service whose
// clusterIP we stripped in buildDestination would be rejected as trying to
// clear an immutable field. The set is the same as droppedSpecFieldsByGVK —
// those entries exist precisely because the apiserver owns them.
func preserveAPIServerAllocatedFields(existing, desired *unstructured.Unstructured) {
	for _, path := range droppedSpecFieldsByGVK[desired.GroupVersionKind()] {
		val, found, err := unstructured.NestedFieldCopy(existing.Object, path...)
		if err != nil || !found {
			continue
		}
		_ = unstructured.SetNestedField(desired.Object, val, path...)
	}
}

// needsUpdate reports whether the existing destination differs from the
// desired one on any field we author. We compare labels, annotations, and
// every top-level field except metadata (apiserver-owned bookkeeping) and
// status (cluster-authoritative). Returning false short-circuits the Update
// call so steady-state reconciles don't emit noisy "Updated" events.
func needsUpdate(existing, desired *unstructured.Unstructured) bool {
	if !reflect.DeepEqual(existing.GetLabels(), desired.GetLabels()) {
		return true
	}
	if !reflect.DeepEqual(existing.GetAnnotations(), desired.GetAnnotations()) {
		return true
	}
	keys := map[string]struct{}{}
	for k := range existing.Object {
		keys[k] = struct{}{}
	}
	for k := range desired.Object {
		keys[k] = struct{}{}
	}
	for k := range keys {
		if k == "metadata" || k == "status" {
			continue
		}
		if !reflect.DeepEqual(existing.Object[k], desired.Object[k]) {
			return true
		}
	}
	return false
}

// writeDestination creates or updates the namespaced Projection's single
// destination object. Returns (reason, message) when the write fails so
// the caller can flip DestinationWritten=False with the right vocabulary.
// reason is "" on success.
func (d *ControllerDeps) writeDestination(
	ctx context.Context,
	proj *projectionv1.Projection,
	source *unstructured.Unstructured,
) (reason, message string) {
	targetNS, _ := destinationCoords(proj)
	dst := buildDestination(source, proj, targetNS)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(source.GroupVersionKind())
	key := client.ObjectKey{Namespace: dst.GetNamespace(), Name: dst.GetName()}
	switch fetchErr := d.Get(ctx, key, existing); {
	case apierrors.IsNotFound(fetchErr):
		if createErr := d.Create(ctx, dst); createErr != nil {
			d.emit(proj, corev1.EventTypeWarning, "DestinationCreateFailed", "Create", createErr.Error())
			return "DestinationCreateFailed", createErr.Error()
		}
		d.emit(proj, corev1.EventTypeNormal, "Projected", "Create",
			fmt.Sprintf("projected %s %s/%s to %s/%s",
				source.GroupVersionKind().String(),
				proj.Spec.Source.Namespace, proj.Spec.Source.Name,
				dst.GetNamespace(), dst.GetName()))
	case fetchErr != nil:
		d.emit(proj, corev1.EventTypeWarning, "DestinationFetchFailed", "Get", fetchErr.Error())
		return "DestinationFetchFailed", fetchErr.Error()
	default:
		if !isOwnedBy(existing, proj) {
			msg := fmt.Sprintf("destination %s/%s exists and is not owned by this Projection",
				key.Namespace, key.Name)
			d.emit(proj, corev1.EventTypeWarning, "DestinationConflict", "Validate", msg)
			return "DestinationConflict", msg
		}
		// Carry over fields the apiserver allocated on create (clusterIP,
		// volumeName, ...) — if we sent dst as-is, an Update of e.g. a Service
		// would be rejected for trying to clear an immutable field.
		preserveAPIServerAllocatedFields(existing, dst)
		if !needsUpdate(existing, dst) {
			// Destination already matches desired state. Skip the Update so
			// we don't generate a noisy "Updated" event/metric on every
			// reconcile of an unchanged Projection.
			return "", ""
		}
		dst.SetResourceVersion(existing.GetResourceVersion())
		if updateErr := d.Update(ctx, dst); updateErr != nil {
			d.emit(proj, corev1.EventTypeWarning, "DestinationUpdateFailed", "Update", updateErr.Error())
			return "DestinationUpdateFailed", updateErr.Error()
		}
		d.emit(proj, corev1.EventTypeNormal, "Updated", "Update",
			fmt.Sprintf("updated %s %s/%s to %s/%s",
				source.GroupVersionKind().String(),
				proj.Spec.Source.Namespace, proj.Spec.Source.Name,
				dst.GetNamespace(), dst.GetName()))
	}
	return "", ""
}
