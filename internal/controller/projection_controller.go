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
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// defaultSelectorWriteConcurrency is the default value for the
// per-Projection in-flight destination-write cap during selector-based
// fan-out. See ProjectionReconciler.SelectorWriteConcurrency for the
// runtime override and the rationale for the cap itself.
const defaultSelectorWriteConcurrency = 16

// ProjectionReconciler reconciles a Projection object.
type ProjectionReconciler struct {
	// ControllerDeps bundles the apiserver-facing dependencies shared with
	// the future ClusterProjectionReconciler (Client, Scheme, DynamicClient,
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

	// SelectorWriteConcurrency bounds the number of in-flight destination
	// writes during selector-based fan-out. Each worker issues a Get plus
	// (optionally) a Create or Update against the apiserver; HTTP/2
	// multiplexing in client-go lets many of these share a single
	// connection, but we cap the parallelism so a Projection matching
	// thousands of namespaces can't DoS the apiserver or blow out
	// controller memory with goroutines. Configured via the
	// --selector-write-concurrency CLI flag (Helm value
	// selectorWriteConcurrency). Defaults to defaultSelectorWriteConcurrency
	// when unset; SetupWithManager fills the zero value so unit-test
	// constructions don't need to set it explicitly, and the fan-out site
	// guards the same default so direct-Reconcile unit tests that bypass
	// SetupWithManager don't deadlock on a zero-capacity semaphore.
	SelectorWriteConcurrency int

	// Controller is the underlying controller.Controller we built in
	// SetupWithManager. We need it so Reconcile can register new source
	// watches lazily as previously-unseen source GVKs show up. It is nil
	// in unit tests that call Reconcile directly without SetupWithManager.
	Controller controller.Controller
	// Cache is the manager's cache, used as the source for dynamic watches.
	// Also nil in direct-reconcile unit tests.
	Cache cache.Cache

	watchedMu sync.Mutex
	watched   map[schema.GroupVersionKind]bool
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

	if handled, result, err := r.handleDeletion(ctx, proj); handled {
		return result, err
	}

	if err := r.ensureFinalizer(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}
	// Fall through rather than requeuing: ensureFinalizer's r.Update mutated
	// proj in place with the apiserver-returned ResourceVersion, so the
	// end-of-reconcile r.Status().Update won't conflict. Avoids a second
	// reconcile round-trip.

	// Mutual exclusion: namespace and namespaceSelector can't both be set.
	// Enforced here rather than via CEL because older apiserver CEL versions
	// (k8s 1.31-) fail to resolve self.namespace for optional string fields.
	if proj.Spec.Destination.Namespace != "" && proj.Spec.Destination.NamespaceSelector != nil {
		msg := "destination.namespace and destination.namespaceSelector are mutually exclusive"
		r.emit(proj, corev1.EventTypeWarning, "InvalidSpec", "Validate", msg)
		return r.failDestination(ctx, proj, "", "InvalidSpec", msg)
	}

	gvr, resolvedVersion, err := r.resolveGVR(proj.Spec.Source)
	if err != nil {
		return r.failSource(ctx, proj, "SourceResolutionFailed", "Resolve", err.Error())
	}

	// Register a watch on this source GVK if we haven't seen it before, so
	// subsequent edits to the source enqueue this (and any other) Projection
	// pointing at it instead of waiting for the next periodic requeue. Key
	// the watch on the *resolved* version (not the unpinned apiVersion the
	// user supplied) so pinned (apps/v1) and unpinned (apps/*) Projections
	// targeting the same Kind share a single watch entry.
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

	// Resolve destination namespace(s).
	destNamespaces, err := r.resolveDestinationNamespaces(ctx, proj)
	if err != nil {
		r.emit(proj, corev1.EventTypeWarning, "NamespaceResolutionFailed", "Resolve", err.Error())
		return r.failDestination(ctx, proj, resolvedVersion, "NamespaceResolutionFailed", err.Error())
	}

	var failures []nsFailure
	successSet := map[string]bool{}

	// Fan out destination writes across a bounded worker pool. Sequential
	// Gets+Updates serialize at ~3-4ms per round-trip, so a Projection
	// matching 100 namespaces spends ~350ms of reconcile time waiting on
	// the apiserver. HTTP/2 multiplexing handles the concurrency at the
	// transport level; we just need to hand it enough work in parallel.
	// The semaphore caps concurrency at SelectorWriteConcurrency so we
	// don't DoS the apiserver with a selector matching thousands of
	// namespaces. The non-positive guard mirrors SetupWithManager's
	// defaulting so unit tests that bypass it (constructing a reconciler
	// directly) don't deadlock on a zero-capacity channel. `mu` guards
	// the two accumulators; contention is minimal (tens of microseconds
	// per reconcile in aggregate), so a single mutex is preferred over
	// sharded accumulators.
	concurrency := r.SelectorWriteConcurrency
	if concurrency <= 0 {
		concurrency = defaultSelectorWriteConcurrency
	}
	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, targetNS := range destNamespaces {
		sem <- struct{}{}
		wg.Add(1)
		go func(ns string) {
			// Defers run LIFO: wg.Done() fires before the semaphore
			// release. Both run even if writeOneDestination panics,
			// which keeps the semaphore from leaking slots and the main
			// goroutine from blocking forever on wg.Wait().
			defer func() { <-sem }()
			defer wg.Done()
			ok, fail := r.writeOneDestination(ctx, proj, source, ns)
			mu.Lock()
			if fail != nil {
				failures = append(failures, *fail)
			}
			if ok {
				successSet[ns] = true
			}
			mu.Unlock()
		}(targetNS)
	}
	wg.Wait()

	// Clean up stale destinations that are no longer in the resolved set.
	if err := r.cleanupStaleDestinations(ctx, proj, gvr, successSet); err != nil {
		logger.Error(err, "cleaning up stale destinations")
		// Non-fatal: log but don't block the reconcile.
	}

	if len(failures) > 0 {
		// Pick the most specific failure reason. If all failures share the
		// same reason (e.g. DestinationConflict), use that; otherwise use a
		// generic rollup reason. No event is emitted here — the inline
		// per-namespace emits in the loop above already surfaced each
		// failure with the right action. failDestination only updates
		// status/conditions/metrics and schedules the requeue.
		reason := failures[0].reason
		for _, f := range failures[1:] {
			if f.reason != reason {
				reason = "DestinationWriteFailed"
				break
			}
		}
		var nsList []string
		for _, f := range failures {
			nsList = append(nsList, f.namespace)
		}
		msg := failures[0].message
		if len(failures) > 1 {
			msg = fmt.Sprintf("failed namespaces: %s", strings.Join(nsList, ", "))
		}
		return r.failDestination(ctx, proj, resolvedVersion, reason, msg)
	}

	if err := r.markAllReady(ctx, proj, resolvedVersion); err != nil {
		return ctrl.Result{}, err
	}
	logBenchStampLatency(ctx, source, "reconcile-end")

	// No periodic requeue: the dynamic source watch registered above is
	// authoritative for propagating future source edits.
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// Two things happen here that Reconcile relies on:
//
//  1. A field indexer on Projection.spec indexes each CR by the canonical
//     sourceKey of its source ref, so mapSource can list all projections
//     referencing a changed source via a single cached List.
//  2. We use .Build(r) (not .Complete(r)) to capture the controller.Controller
//     so Reconcile can lazily register new source watches as previously-unseen
//     GVKs appear. No up-front source watches — Reconcile adds them on demand.
//  3. A Namespace watch triggers re-reconciliation of selector-based Projections
//     whenever the set of namespaces changes.
func (r *ProjectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.RequeueInterval == 0 {
		r.RequeueInterval = 30 * time.Second
	}
	if r.SelectorWriteConcurrency <= 0 {
		r.SelectorWriteConcurrency = defaultSelectorWriteConcurrency
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &projectionv1.Projection{}, sourceIndex,
		func(obj client.Object) []string {
			p, ok := obj.(*projectionv1.Projection)
			if !ok {
				return nil
			}
			gv, err := schema.ParseGroupVersion(p.Spec.Source.APIVersion)
			if err != nil {
				// Malformed apiVersion — admission should reject this, but if it
				// ever slips through, indexing under "" rather than panicking
				// keeps the controller alive. Reconcile will surface the error
				// via SourceResolutionFailed; the V(1) log here is defense-in-depth
				// for cases where admission silently failed-open (CRD removed,
				// schema migration, manual etcd write).
				ctrl.Log.WithName("source-indexer").V(1).Info(
					"skipping index entry for malformed apiVersion",
					"projection", client.ObjectKeyFromObject(p), "apiVersion", p.Spec.Source.APIVersion)
				return nil
			}
			return []string{sourceKey(
				gv.Group,
				p.Spec.Source.Kind,
				p.Spec.Source.Namespace,
				p.Spec.Source.Name,
			)}
		}); err != nil {
		return fmt.Errorf("registering source field indexer: %w", err)
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &projectionv1.Projection{}, selectorIndex,
		func(obj client.Object) []string {
			p, ok := obj.(*projectionv1.Projection)
			if !ok {
				return nil
			}
			if p.Spec.Destination.NamespaceSelector != nil {
				return []string{selectorIndexValue}
			}
			return nil
		}); err != nil {
		return fmt.Errorf("registering selector field indexer: %w", err)
	}

	c, err := builder.ControllerManagedBy(mgr).
		For(&projectionv1.Projection{}).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespace)).
		Build(r)
	if err != nil {
		return err
	}
	r.Controller = c
	r.Cache = mgr.GetCache()
	r.Recorder = mgr.GetEventRecorder("projection-controller")
	return nil
}
