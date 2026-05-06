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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// Field-indexer keys registered on Projection in SetupWithManager. mapSource
// looks up Projections by sourceIndex.
const (
	// sourceIndex is the field-indexer key we register on Projection so that
	// a source-object event can be mapped to all Projections pointing at it
	// via a single cached List(MatchingFields).
	sourceIndex = "spec.sourceKey"
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
