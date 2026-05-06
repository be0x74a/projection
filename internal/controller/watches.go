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
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// Field-indexer keys registered on Projection in SetupWithManager. mapSource
// looks up Projections by sourceIndex.
const (
	// sourceIndex is the field-indexer key we register on Projection (and
	// ClusterProjection — different per-CR registration, same key) so that
	// a source-object event can be mapped to all Projections pointing at it
	// via a single cached List(MatchingFields).
	sourceIndex = "spec.sourceKey"

	// uidIndex is the field-indexer key we register on each CR type
	// (Projection, ClusterProjection) on metadata.uid. ensureDestWatch
	// resolves a destination's UID-label value to its owner via a single
	// cached List(MatchingFields) on this index — O(1) regardless of how
	// many Projections live in the cluster.
	uidIndex = "metadata.uid"
)

// ensureSourceWatch registers a dynamic watch on the given source GVK if one
// is not already in place. Events from the watch are mapped back to every
// Projection pointing at the changed object via the field indexer on
// sourceIndex.
//
// The controller is nil in unit tests that call Reconcile directly (no
// SetupWithManager, no running manager) — in that case we no-op.
func (r *ProjectionReconciler) ensureSourceWatch(gvk schema.GroupVersionKind) error {
	if r.Controller == nil || r.Cache == nil {
		return nil
	}
	r.watchedMu.Lock()
	defer r.watchedMu.Unlock()
	if r.watched == nil {
		r.watched = map[schema.GroupVersionKind]bool{}
	}
	if r.watched[gvk] {
		return nil
	}
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(gvk)
	src := source.Kind(r.Cache, client.Object(obj),
		handler.TypedEnqueueRequestsFromMapFunc[client.Object](r.mapSource))
	if err := r.Controller.Watch(src); err != nil {
		return err
	}
	r.watched[gvk] = true
	watchedGvks.Inc()
	return nil
}

// mapSource translates a source-object event into reconcile.Requests for
// every Projection that references it. The field indexer keyed on
// sourceKey lets us do this with a single cached List.
func (r *ProjectionReconciler) mapSource(ctx context.Context, obj client.Object) []reconcile.Request {
	gvk := obj.GetObjectKind().GroupVersionKind()
	key := sourceKey(projectionv1.SourceRef{
		Group:     gvk.Group,
		Kind:      gvk.Kind,
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	})
	var list projectionv1.ProjectionList
	if err := r.List(ctx, &list, client.MatchingFields{sourceIndex: key}); err != nil {
		log.FromContext(ctx).Error(err, "listing projections by source index", "key", key)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		reqs = append(reqs, reconcile.Request{
			NamespacedName: client.ObjectKey{Namespace: p.Namespace, Name: p.Name},
		})
	}
	return reqs
}

// ensureDestWatch registers a label-filtered dynamic watch on the given
// destination GVK if one is not already in place. Used by both the
// namespaced and cluster reconcilers so a manual `kubectl delete` of a
// projected object triggers immediate reconciliation rather than waiting
// for the next periodic resync.
//
// uidLabel is the label key whose presence marks an object as owned by
// some incarnation of this reconciler's CR type (e.g.
// "projection.sh/owned-by-projection-uid"). The watch source pre-filters
// via PartialObjectMetadata plus a label-existence predicate, so we don't
// pay deserialization cost on every random object change in the cluster.
//
// enqueueByUID maps a UID-label value back to a single reconcile.Request
// via the per-CR-type metadata.uid field indexer. Returns nil if no
// owner is found (label was stale or this is a sibling reconciler's
// object).
//
// The annotation is the authoritative ownership signal — the label is only
// a watch hint. enqueueByUID can return false positives if a stranger
// stamps our label on their object; the resulting reconcile then no-ops
// at writeDestination because isOwnedBy / isOwnedByCluster checks the
// annotation. So a misleading label costs at most one wasted reconcile,
// not data corruption.
func (deps *ControllerDeps) ensureDestWatch(
	ctrlr controller.Controller,
	cch cache.Cache,
	gvk schema.GroupVersionKind,
	uidLabel string,
	enqueueByUID func(ctx context.Context, uid string) []reconcile.Request,
	watched map[schema.GroupVersionKind]bool,
	mu *sync.Mutex,
) error {
	if ctrlr == nil || cch == nil {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	if watched[gvk] {
		return nil
	}

	// PartialObjectMetadata so the watch only deserializes object metadata,
	// not the full payload. We only need labels to filter and metadata.name
	// to look up the owner.
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(gvk)

	// Label-existence predicate: forward only events for objects carrying
	// the UID label key. The label value is dereferenced to an owner
	// inside the handler.
	pred := predicate.NewTypedPredicateFuncs[client.Object](func(o client.Object) bool {
		_, ok := o.GetLabels()[uidLabel]
		return ok
	})

	src := source.Kind(cch, client.Object(obj),
		handler.TypedEnqueueRequestsFromMapFunc[client.Object](
			func(ctx context.Context, o client.Object) []reconcile.Request {
				uid := o.GetLabels()[uidLabel]
				if uid == "" {
					return nil
				}
				return enqueueByUID(ctx, uid)
			}),
		pred)

	if err := ctrlr.Watch(src); err != nil {
		return err
	}
	watched[gvk] = true
	watchedDestGvks.Inc()
	return nil
}
