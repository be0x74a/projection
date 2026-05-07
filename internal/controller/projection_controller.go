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
	"strconv"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// ProjectionReconciler reconciles a (namespaced) Projection object. The
// Projection mirrors a single source object into the Projection's own
// namespace; cross-namespace fan-out lives on the cluster-scoped sibling
// (ClusterProjection) and its dedicated reconciler.
type ProjectionReconciler struct {
	// ControllerDeps bundles the apiserver-facing dependencies shared with
	// ClusterProjectionReconciler (Client, Scheme, DynamicClient,
	// RESTMapper, Recorder). Embedded so `r.Client`, `r.Scheme`, etc. continue
	// to resolve via promotion and existing callsites read unchanged. Pointer
	// embedding lets cmd/main.go and tests build the reconciler with a single
	// composite literal where the apiserver dependencies live behind one field.
	*ControllerDeps

	// SourceMode is the cluster-admin-configured policy for which source
	// objects are projectable. Empty string defaults to SourceModeAllowlist.
	SourceMode SourceMode

	// RequeueInterval controls how long the reconciler sleeps before
	// retrying after a successful or failed reconcile. Configured via the
	// --requeue-interval CLI flag. Defaults to 30 seconds when unset
	// (SetupWithManager fills the zero value so unit-test constructions
	// don't need to set it explicitly).
	RequeueInterval time.Duration

	// Controller is the underlying controller.Controller we built in
	// SetupWithManager. We need it so Reconcile can register new source
	// watches lazily as previously-unseen source GVKs show up. It is nil
	// in unit tests that call Reconcile directly without SetupWithManager.
	Controller controller.Controller
	// Cache is the manager's cache, used as the source for dynamic watches.
	// Also nil in direct-reconcile unit tests.
	Cache cache.Cache

	// Source-watch state. ensureSourceWatch deduplicates so we register at
	// most one watch per source GVK regardless of how many Projections
	// converge on the same Kind.
	watchedMu sync.Mutex
	watched   map[schema.GroupVersionKind]bool

	// Destination-watch state. ensureDestWatch registers a label-filtered
	// watch on the destination GVK so a manual `kubectl delete` of a
	// projected object triggers immediate reconciliation. Same dedupe
	// shape as the source-watch map.
	watchedDestsMu sync.Mutex
	watchedDests   map[schema.GroupVersionKind]bool
}

// benchStampAnnotation is the annotation the projection benchmark harness
// writes on source objects to measure end-to-end propagation latency. Value
// is a unix-nano timestamp. Presence of the annotation triggers a per-phase
// latency log line in Reconcile; absence makes this a no-op in production.
const benchStampAnnotation = "bench.projection.sh/stamp"

// logBenchStampLatency, when the source carries the benchmark harness's
// stamp annotation, logs the wall-clock delta from stamp issuance at the
// named reconcile phase. Used only by the bench harness to decompose the
// observed e2e latency floor. No-op when the annotation is absent.
func logBenchStampLatency(ctx context.Context, source *unstructured.Unstructured, phase string) {
	v := source.GetAnnotations()[benchStampAnnotation]
	if v == "" {
		return
	}
	nanos, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return
	}
	delta := time.Since(time.Unix(0, nanos))
	log.FromContext(ctx).Info("bench stamp latency",
		"phase", phase,
		"delta_ms", delta.Milliseconds(),
		"name", source.GetName(),
		"namespace", source.GetNamespace(),
	)
}

// +kubebuilder:rbac:groups=projection.sh,resources=projections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=projection.sh,resources=projections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=projection.sh,resources=projections/finalizers,verbs=update
// Projection can mirror any Kind, so the controller needs broad access.
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete

func (r *ProjectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	proj := &projectionv1.Projection{}
	if err := r.Get(ctx, req.NamespacedName, proj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if handled, err := r.handleDeletion(ctx, proj); handled {
		return ctrl.Result{}, err
	}

	if err := r.ensureFinalizer(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}
	// Fall through rather than requeuing: ensureFinalizer's r.Update mutated
	// proj in place with the apiserver-returned ResourceVersion, so the
	// end-of-reconcile r.Status().Update won't conflict. Avoids a second
	// reconcile round-trip.

	gvr, resolvedVersion, err := r.resolveGVR(proj.Spec.Source)
	if err != nil {
		return r.failSource(ctx, proj, "SourceResolutionFailed", "Resolve", err.Error())
	}

	// Register a watch on this source GVK if we haven't seen it before, so
	// subsequent edits to the source enqueue this (and any other) Projection
	// pointing at it instead of waiting for the next periodic requeue. Key
	// the watch on the *resolved* version (not the spec version the user
	// supplied) so pinned (version: v1) and unpinned (version omitted)
	// Projections targeting the same Kind share a single watch entry.
	watchGVK := schema.GroupVersionKind{
		Group:   gvr.Group,
		Version: resolvedVersion,
		Kind:    proj.Spec.Source.Kind,
	}
	if err := r.ensureSourceWatch(watchGVK); err != nil {
		logger.Error(err, "registering source watch", "gvk", watchGVK)
		// Don't fail the reconcile: the periodic-requeue-on-error path below
		// still keeps us alive, and a future reconcile will retry the watch.
	}

	source, err := r.DynamicClient.Resource(gvr).
		Namespace(proj.Spec.Source.Namespace).
		Get(ctx, proj.Spec.Source.Name, metav1.GetOptions{})
	if err != nil {
		return r.handleSourceFetchError(ctx, proj, gvr, err)
	}
	logBenchStampLatency(ctx, source, "source-fetched")

	// Opt-in / opt-out policy. If the source says it's not projectable (or
	// didn't opt in under allowlist mode), clean up any destination we
	// previously created and stop.
	if reason, msg, ok := r.checkSourceProjectable(source); !ok {
		if delErr := r.deleteDestination(ctx, proj); delErr != nil {
			logger.Error(delErr, "cleaning up destination after opt-out",
				"reason", reason)
			// Don't fail on cleanup error — log and surface the policy
			// failure as the primary reason.
		}
		return r.failSource(ctx, proj, reason, "Validate", msg)
	}

	if reason, msg := r.writeDestination(ctx, proj, source); reason != "" {
		return r.failDestination(ctx, proj, resolvedVersion, reason, msg)
	}

	// Register the dest-side watch on the success path (the failure path
	// returns above via failDestination, which doesn't register a watch).
	// Any subsequent manual `kubectl delete` (or stranger overwrite) of
	// the projected object now triggers an immediate reconcile via the
	// watch rather than waiting for the periodic resync.
	if err := r.ensureNamespacedDestWatch(watchGVK); err != nil {
		logger.Error(err, "registering destination watch", "gvk", watchGVK)
	}

	if err := r.markAllReady(ctx, proj, resolvedVersion); err != nil {
		return ctrl.Result{}, err
	}
	logBenchStampLatency(ctx, source, "reconcile-end")

	// No periodic requeue: the dynamic source watch registered above is
	// authoritative for propagating future source edits.
	return ctrl.Result{}, nil
}

// ensureNamespacedDestWatch wires the shared ensureDestWatch helper for the
// namespaced UID label and field indexer.
func (r *ProjectionReconciler) ensureNamespacedDestWatch(gvk schema.GroupVersionKind) error {
	if r.watchedDests == nil {
		r.watchedDestsMu.Lock()
		if r.watchedDests == nil {
			r.watchedDests = map[schema.GroupVersionKind]bool{}
		}
		r.watchedDestsMu.Unlock()
	}
	return r.ensureDestWatch(r.Controller, r.Cache, gvk, ownedByUIDLabel,
		r.enqueueByUID, r.watchedDests, &r.watchedDestsMu)
}

// enqueueByUID resolves a UID-label value back to the owning Projection's
// namespaced name via the metadata.uid field indexer. The indexer keeps
// the lookup O(1) regardless of cluster size; without it we'd have to
// walk every Projection in the cluster on every dest event.
func (r *ProjectionReconciler) enqueueByUID(ctx context.Context, uid string) []reconcile.Request {
	if uid == "" {
		return nil
	}
	var list projectionv1.ProjectionList
	if err := r.List(ctx, &list, client.MatchingFields{uidIndex: uid}); err != nil {
		log.FromContext(ctx).Error(err, "listing projections by uid index", "uid", uid)
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

// SetupWithManager sets up the controller with the Manager.
//
// Three things happen here that Reconcile relies on:
//
//  1. A field indexer on Projection.spec indexes each CR by the canonical
//     sourceKey of its source ref, so mapSource can list all projections
//     referencing a changed source via a single cached List.
//  2. A field indexer on Projection.metadata.uid lets ensureDestWatch's
//     handler resolve a destination's UID-label value back to its owner
//     in O(1) (single cached List, no full-cluster scan).
//  3. We use .Build(r) (not .Complete(r)) to capture the controller.Controller
//     so Reconcile can lazily register source / destination watches as
//     previously-unseen GVKs appear. No up-front watches — Reconcile adds
//     them on demand.
func (r *ProjectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.RequeueInterval == 0 {
		r.RequeueInterval = 30 * time.Second
	}
	r.watched = map[schema.GroupVersionKind]bool{}
	r.watchedDests = map[schema.GroupVersionKind]bool{}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &projectionv1.Projection{}, sourceIndex,
		func(obj client.Object) []string {
			p, ok := obj.(*projectionv1.Projection)
			if !ok {
				return nil
			}
			return []string{sourceKey(p.Spec.Source)}
		}); err != nil {
		return fmt.Errorf("registering source field indexer: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &projectionv1.Projection{}, uidIndex,
		func(obj client.Object) []string {
			p, ok := obj.(*projectionv1.Projection)
			if !ok {
				return nil
			}
			return []string{string(p.UID)}
		}); err != nil {
		return fmt.Errorf("registering projection uid field indexer: %w", err)
	}

	c, err := builder.ControllerManagedBy(mgr).
		For(&projectionv1.Projection{}).
		Build(r)
	if err != nil {
		return err
	}
	r.Controller = c
	r.Cache = mgr.GetCache()
	r.Recorder = mgr.GetEventRecorder("projection-controller")
	return nil
}
