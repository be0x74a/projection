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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// deleteDestination removes the namespaced Projection's single owned
// destination if it still exists and our ownership annotation is still
// present. Returns nil for the source-no-longer-resolvable case so the
// finalizer can still complete — losing the source Kind shouldn't trap
// the Projection in Terminating forever.
func (d *ControllerDeps) deleteDestination(ctx context.Context, proj *projectionv1.Projection) error {
	gvr, _, err := d.resolveGVR(proj.Spec.Source)
	if err != nil {
		// Source Kind no longer resolves — we can't locate the destination.
		// Proceed with finalizer removal rather than blocking forever.
		return nil
	}
	ns, name := destinationCoords(proj)
	return d.deleteOneDestination(ctx, proj, gvr, ns, name)
}

// deleteOneDestination removes a single destination if it's owned by this Projection.
func (d *ControllerDeps) deleteOneDestination(ctx context.Context, proj *projectionv1.Projection, gvr schema.GroupVersionResource, ns, name string) error {
	existing, err := d.DynamicClient.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isOwnedBy(existing, proj) {
		d.emit(proj, corev1.EventTypeNormal, "DestinationLeftAlone", "Delete",
			fmt.Sprintf("%s/%s (not owned)", ns, name))
		return nil
	}
	err = d.DynamicClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	d.emit(proj, corev1.EventTypeNormal, "DestinationDeleted", "Delete",
		fmt.Sprintf("%s/%s", ns, name))
	return nil
}

// deleteAllOwnedDestinations deletes every destination owned by this
// Projection. Today the namespaced reconciler only ever writes one
// destination, so this is functionally equivalent to deleteOneDestination —
// but the source-deleted path through handleSourceFetchError still calls
// it, and keeping the implementation label-driven means it stays correct
// if a future migration leaves stragglers in unexpected namespaces.
//
// Finds destinations via a single cluster-wide List on the destination GVK
// filtered by the ownedByUIDLabel — O(owned) instead of O(all cluster
// namespaces). The annotation is still checked as a belt-and-braces
// ownership guard in case a stranger has manually set the label.
func (d *ControllerDeps) deleteAllOwnedDestinations(ctx context.Context, proj *projectionv1.Projection, gvr schema.GroupVersionResource) error {
	ownerValue := ownerKey(proj)

	owned, err := d.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{
		LabelSelector: ownedByUIDSelector(proj),
	})
	if err != nil {
		return fmt.Errorf("listing owned destinations: %w", err)
	}
	var firstErr error
	for i := range owned.Items {
		obj := &owned.Items[i]
		ns := obj.GetNamespace()
		name := obj.GetName()
		// Belt-and-braces: verify the annotation too. The label alone is
		// enough for a cooperative system; checking the annotation protects
		// against a malicious or buggy actor copying our label to a
		// stranger's object and tricking the controller into deleting it.
		if obj.GetAnnotations()[ownedByAnnotation] != ownerValue {
			continue
		}
		if err := d.DynamicClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		d.emit(proj, corev1.EventTypeNormal, "DestinationDeleted", "Delete",
			fmt.Sprintf("%s/%s", ns, name))
	}
	return firstErr
}

// deleteAllClusterOwnedDestinations is the cluster-tier sibling of
// deleteAllOwnedDestinations. Lists every destination across every namespace
// carrying this ClusterProjection's UID label, then deletes each whose
// authoritative cluster-tier annotation also matches. Used by the cluster
// finalizer (cleanup on CR delete) and by cleanupStaleClusterDestinations
// (transitive cleanup when a namespace stops matching the selector).
//
// Returns the first delete error encountered, after attempting every
// candidate, so a single transient apiserver hiccup doesn't wedge a
// finalizer on the unaffected namespaces.
func (d *ControllerDeps) deleteAllClusterOwnedDestinations(ctx context.Context, cp *projectionv1.ClusterProjection, gvr schema.GroupVersionResource) error {
	return d.deleteClusterOwnedDestinations(ctx, cp, gvr, nil)
}

// deleteClusterOwnedDestinations is the workhorse for both the
// "delete-all" and "delete-stale" paths. When keep is nil, every owned
// destination is deleted (finalizer cleanup). When keep is non-nil, every
// owned destination whose namespace is NOT in keep is deleted
// (stale-namespace pruning when a selector stops matching a namespace).
func (d *ControllerDeps) deleteClusterOwnedDestinations(
	ctx context.Context,
	cp *projectionv1.ClusterProjection,
	gvr schema.GroupVersionResource,
	keep map[string]struct{},
) error {
	ownerValue := clusterOwnerKey(cp)

	owned, err := d.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{
		LabelSelector: ownedByClusterUIDSelector(cp),
	})
	if err != nil {
		return fmt.Errorf("listing owned destinations: %w", err)
	}
	var firstErr error
	for i := range owned.Items {
		obj := &owned.Items[i]
		ns := obj.GetNamespace()
		name := obj.GetName()
		if obj.GetAnnotations()[ownedByClusterAnnotation] != ownerValue {
			continue
		}
		if keep != nil {
			if _, retain := keep[ns]; retain {
				continue
			}
		}
		if err := d.DynamicClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		d.emit(cp, corev1.EventTypeNormal, "DestinationDeleted", "Delete",
			fmt.Sprintf("%s/%s", ns, name))
	}
	return firstErr
}

// cleanupStaleClusterDestinations removes destinations owned by cp whose
// namespace is no longer in the resolved target set. Called from the
// cluster reconciler's main path after the fan-out write. Best-effort: a
// list error is logged by the caller but does not block the rollup.
func (d *ControllerDeps) cleanupStaleClusterDestinations(
	ctx context.Context,
	cp *projectionv1.ClusterProjection,
	gvr schema.GroupVersionResource,
	currentTargets []string,
) error {
	keep := make(map[string]struct{}, len(currentTargets))
	for _, ns := range currentTargets {
		keep[ns] = struct{}{}
	}
	return d.deleteClusterOwnedDestinations(ctx, cp, gvr, keep)
}

// deleteClusterDestination is the cluster-tier sibling of deleteDestination —
// removes every owned destination across all targeted namespaces. Used
// inside handleClusterDeletion. Returns nil for the source-no-longer-
// resolvable case so the cluster finalizer can complete.
func (d *ControllerDeps) deleteClusterDestination(ctx context.Context, cp *projectionv1.ClusterProjection) error {
	gvr, _, err := d.resolveGVR(cp.Spec.Source)
	if err != nil {
		return nil
	}
	return d.deleteAllClusterOwnedDestinations(ctx, cp, gvr)
}
