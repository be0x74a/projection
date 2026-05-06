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

func (d *ControllerDeps) deleteDestination(ctx context.Context, proj *projectionv1.Projection) error {
	gvr, _, err := d.resolveGVR(proj.Spec.Source)
	if err != nil {
		// Source Kind no longer resolves — we can't locate the destination.
		// Proceed with finalizer removal rather than blocking forever.
		return nil
	}

	// For selector-based Projections (or any Projection that may have previously
	// used a selector), scan all namespaces for owned destinations.
	if proj.Spec.Destination.NamespaceSelector != nil {
		return d.deleteAllOwnedDestinations(ctx, proj, gvr)
	}

	// Single-namespace path.
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
// Projection. Used by the finalizer path for selector-based Projections,
// where the selector may have changed since the last reconcile and we need
// to sweep across all namespaces the Projection ever wrote to.
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

// cleanupStaleDestinations removes destinations in namespaces that are owned
// by this Projection but are no longer in the current resolved set. This
// handles the case where a namespace loses a label or the selector changes.
// O(owned destinations) per call via a single cluster-wide List filtered by
// the ownedByUIDLabel — independent of total cluster namespace count (#33).
// Runs only for selector-based Projections.
func (d *ControllerDeps) cleanupStaleDestinations(ctx context.Context, proj *projectionv1.Projection, gvr schema.GroupVersionResource, currentSet map[string]bool) error {
	// Only needed for selector-based Projections.
	if proj.Spec.Destination.NamespaceSelector == nil {
		return nil
	}
	ownerValue := ownerKey(proj)

	owned, err := d.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{
		LabelSelector: ownedByUIDSelector(proj),
	})
	if err != nil {
		return fmt.Errorf("listing owned destinations for stale cleanup: %w", err)
	}
	var firstErr error
	for i := range owned.Items {
		obj := &owned.Items[i]
		ns := obj.GetNamespace()
		if currentSet[ns] {
			continue // still in the resolved set, keep it
		}
		// Belt-and-braces ownership check; see deleteAllOwnedDestinations
		// for why the label alone isn't sufficient when the namespace is
		// controlled by someone other than us.
		if obj.GetAnnotations()[ownedByAnnotation] != ownerValue {
			continue
		}
		name := obj.GetName()
		if err := d.DynamicClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		d.emit(proj, corev1.EventTypeNormal, "StaleDestinationDeleted", "Delete",
			fmt.Sprintf("%s/%s", ns, name))
	}
	return firstErr
}
