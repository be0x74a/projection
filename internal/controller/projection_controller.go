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
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

const (
	finalizerName         = "projection.sh/finalizer"
	ownedByAnnotation     = "projection.sh/owned-by"
	projectableAnnotation = "projection.sh/projectable"

	// ownedByUIDLabel is a label stamped on every destination by
	// buildDestination. Value is the owning Projection's UID. Enables
	// cleanup paths to locate owned destinations via a single cluster-wide
	// List(LabelSelector) instead of walking every namespace in the cluster
	// (#33). Label values are capped at 63 chars and permit [a-z0-9-] plus
	// dashes; Kubernetes UIDs are RFC-4122 UUIDs (36 chars), both within
	// the label-value regex and well under the length limit.
	ownedByUIDLabel = "projection.sh/owned-by-uid"

	conditionReady              = "Ready"
	conditionSourceResolved     = "SourceResolved"
	conditionDestinationWritten = "DestinationWritten"

	// sourceIndex is the field-indexer key we register on Projection so that
	// a source-object event can be mapped to all Projections pointing at it
	// via a single cached List(MatchingFields).
	sourceIndex = "spec.sourceKey"

	// selectorIndex is a synthetic field-indexer key for Projections that use
	// a namespaceSelector. It lets mapNamespace efficiently find only
	// selector-based Projections instead of listing all Projections.
	selectorIndex      = "spec.hasNamespaceSelector"
	selectorIndexValue = "true"

	// defaultSelectorWriteConcurrency is the default value for the
	// per-Projection in-flight destination-write cap during selector-based
	// fan-out. See ProjectionReconciler.SelectorWriteConcurrency for the
	// runtime override and the rationale for the cap itself.
	defaultSelectorWriteConcurrency = 16
)

// nsFailure records a single per-namespace destination-write failure during
// selector fan-out. The rollup block after the worker pool inspects these to
// pick the most specific reason for the DestinationWritten=False condition.
type nsFailure struct {
	namespace string
	reason    string
	message   string
}

// SourceMode controls which source objects the operator is willing to
// project. Configured once per controller via the --source-mode flag.
type SourceMode string

const (
	// SourceModePermissive allows any source object to be projected. Source
	// owners can still veto individual objects with the
	// projection.sh/projectable="false" annotation.
	SourceModePermissive SourceMode = "permissive"

	// SourceModeAllowlist requires every source object to carry the
	// projection.sh/projectable="true" annotation before it can be
	// mirrored. This is the default — Kubernetes convention favors
	// opt-in for cluster-scoped operators with broad read RBAC.
	SourceModeAllowlist SourceMode = "allowlist"
)

// ProjectionReconciler reconciles a Projection object.
type ProjectionReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	DynamicClient dynamic.Interface
	RESTMapper    apimeta.RESTMapper
	Recorder      events.EventRecorder

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

// emit records a Kubernetes Event against the Projection. Nil-safe so unit
// tests that build a reconciler directly (without SetupWithManager) don't
// need to plumb a recorder. action is the UpperCamelCase verb describing
// what the controller did (e.g. Create, Update, Resolve); reason is the
// categorical outcome tag.
func (r *ProjectionReconciler) emit(proj *projectionv1.Projection, eventType, reason, action, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(proj, nil, eventType, reason, action, "%s", message)
}

// sourceKey is the canonical string key identifying a source object across
// both the field indexer and the event-mapping function. The version is
// intentionally omitted: source events always carry a resolved GVK, but a
// Projection may reference its source via an unpinned form (e.g. apps/*).
// Joining on (group, kind, namespace, name) keeps both sides in agreement
// regardless of which served version the apiserver delivered the event for.
func sourceKey(group, kind, namespace, name string) string {
	return group + "/" + kind + "/" + namespace + "/" + name
}

// resolvedVersionMessage produces the human-readable SourceResolved
// condition message when a Projection used the unpinned form (apps/*) and
// the RESTMapper picked a concrete version. Returns "" for pinned sources
// to preserve today's empty-message behavior.
func resolvedVersionMessage(src projectionv1.SourceRef, resolvedVersion string) string {
	gv, err := schema.ParseGroupVersion(src.APIVersion)
	// resolvedVersion == "" means failDestination was called before
	// resolveGVR ran (e.g. from the InvalidSpec mutex-violation path);
	// there's no version to report yet.
	if err != nil || gv.Version != "*" || resolvedVersion == "" {
		return ""
	}
	return fmt.Sprintf("resolved %s/%s to preferred version %s",
		gv.Group, src.Kind, resolvedVersion)
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

	if !proj.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(proj, finalizerName) {
			if err := r.deleteDestination(ctx, proj); err != nil {
				logger.Error(err, "deleting destination")
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(proj, finalizerName)
			if err := r.Update(ctx, proj); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(proj, finalizerName) {
		controllerutil.AddFinalizer(proj, finalizerName)
		if err := r.Update(ctx, proj); err != nil {
			return ctrl.Result{}, err
		}
		// Fall through rather than requeuing: r.Update mutated proj in place
		// with the apiserver-returned ResourceVersion, so the end-of-reconcile
		// r.Status().Update won't conflict. Avoids a second reconcile round-trip.
	}

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
	if err := r.ensureWatch(watchGVK); err != nil {
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

// writeOneDestination reconciles a single destination namespace for proj.
// It is safe to invoke concurrently for distinct targetNS values — the method
// only reads the shared inputs (proj, source) and writes to the apiserver;
// all per-reconcile accumulator state is returned to the caller. Events are
// emitted here rather than in the caller so each worker surfaces its own
// outcome without cross-goroutine coordination; the controller-runtime event
// recorder is itself thread-safe.
//
// Returned (ok, failure) are orthogonal:
//
//   - ok=true, failure=nil: destination left in the desired state (created,
//     updated, or already matching). Caller adds targetNS to successSet.
//   - ok=false, failure=non-nil: per-namespace failure. Caller appends to
//     the failures slice; the rollup block after the worker pool picks a
//     reason and calls failDestination.
//
// ok=true with failure=non-nil is not a valid combination and the caller
// is free to treat only one of the two.
func (r *ProjectionReconciler) writeOneDestination(
	ctx context.Context,
	proj *projectionv1.Projection,
	source *unstructured.Unstructured,
	targetNS string,
) (ok bool, failure *nsFailure) {
	dst := buildDestination(source, proj, targetNS)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(source.GroupVersionKind())
	key := client.ObjectKey{Namespace: dst.GetNamespace(), Name: dst.GetName()}
	switch fetchErr := r.Get(ctx, key, existing); {
	case apierrors.IsNotFound(fetchErr):
		if createErr := r.Create(ctx, dst); createErr != nil {
			r.emit(proj, corev1.EventTypeWarning, "DestinationCreateFailed", "Create",
				fmt.Sprintf("failed to create in %s: %s", targetNS, createErr.Error()))
			return false, &nsFailure{targetNS, "DestinationCreateFailed", createErr.Error()}
		}
		r.emit(proj, corev1.EventTypeNormal, "Projected", "Create",
			fmt.Sprintf("projected %s %s/%s to %s/%s",
				source.GroupVersionKind().String(),
				proj.Spec.Source.Namespace, proj.Spec.Source.Name,
				dst.GetNamespace(), dst.GetName()))
	case fetchErr != nil:
		r.emit(proj, corev1.EventTypeWarning, "DestinationFetchFailed", "Get",
			fmt.Sprintf("failed to fetch in %s: %s", targetNS, fetchErr.Error()))
		return false, &nsFailure{targetNS, "DestinationFetchFailed", fetchErr.Error()}
	default:
		if !isOwnedBy(existing, proj) {
			msg := fmt.Sprintf("destination %s/%s exists and is not owned by this Projection",
				key.Namespace, key.Name)
			r.emit(proj, corev1.EventTypeWarning, "DestinationConflict", "Validate", msg)
			return false, &nsFailure{targetNS, "DestinationConflict", msg}
		}
		// Carry over fields the apiserver allocated on create (clusterIP,
		// volumeName, ...) — if we sent dst as-is, an Update of e.g. a Service
		// would be rejected for trying to clear an immutable field.
		preserveAPIServerAllocatedFields(existing, dst)
		if !needsUpdate(existing, dst) {
			// Destination already matches desired state. Skip the Update so
			// we don't generate a noisy "Updated" event/metric on every
			// reconcile of an unchanged Projection. Still considered ok so
			// the caller records it in successSet (keeps it out of the
			// stale-cleanup set).
			return true, nil
		}
		dst.SetResourceVersion(existing.GetResourceVersion())
		if updateErr := r.Update(ctx, dst); updateErr != nil {
			r.emit(proj, corev1.EventTypeWarning, "DestinationUpdateFailed", "Update",
				fmt.Sprintf("failed to update in %s: %s", targetNS, updateErr.Error()))
			return false, &nsFailure{targetNS, "DestinationUpdateFailed", updateErr.Error()}
		}
		r.emit(proj, corev1.EventTypeNormal, "Updated", "Update",
			fmt.Sprintf("updated %s %s/%s to %s/%s",
				source.GroupVersionKind().String(),
				proj.Spec.Source.Namespace, proj.Spec.Source.Name,
				dst.GetNamespace(), dst.GetName()))
	}
	return true, nil
}

// ensureWatch registers a dynamic watch on the given source GVK if one is not
// already in place. Events from the watch are mapped back to every Projection
// pointing at the changed object via the field indexer on sourceIndex.
//
// The controller is nil in unit tests that call Reconcile directly (no
// SetupWithManager, no running manager) — in that case we no-op.
func (r *ProjectionReconciler) ensureWatch(gvk schema.GroupVersionKind) error {
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
// every Projection that references it. The field indexer keyed on sourceKey
// lets us do this with a single cached List.
func (r *ProjectionReconciler) mapSource(ctx context.Context, obj client.Object) []reconcile.Request {
	gvk := obj.GetObjectKind().GroupVersionKind()
	key := sourceKey(gvk.Group, gvk.Kind, obj.GetNamespace(), obj.GetName())
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

// resolveGVR maps a SourceRef's apiVersion+kind to a concrete GVR via the
// cached RESTMapper. The second return value is the version the RESTMapper
// picked — equal to gv.Version when the user pinned a version, or the
// preferred served version when unpinned (gv.Version == "*"). Callers
// surface the resolved version in the SourceResolved condition message
// for operator-visibility.
func (r *ProjectionReconciler) resolveGVR(src projectionv1.SourceRef) (schema.GroupVersionResource, string, error) {
	gv, err := schema.ParseGroupVersion(src.APIVersion)
	if err != nil {
		return schema.GroupVersionResource{}, "", fmt.Errorf("parsing apiVersion %q: %w", src.APIVersion, err)
	}
	gk := schema.GroupKind{Group: gv.Group, Kind: src.Kind}

	var mapping *apimeta.RESTMapping
	switch {
	case gv.Version == "*" && gv.Group == "":
		return schema.GroupVersionResource{}, "", fmt.Errorf(
			"apiVersion %q: group is required when version is unpinned", src.APIVersion)
	case gv.Version == "*":
		// Unpinned: RESTMapper picks the preferred served version.
		mapping, err = r.RESTMapper.RESTMapping(gk)
	default:
		// Pinned to a specific version (today's behavior).
		mapping, err = r.RESTMapper.RESTMapping(gk, gv.Version)
	}
	if err != nil {
		return schema.GroupVersionResource{}, "", fmt.Errorf("resolving %s/%s: %w", src.APIVersion, src.Kind, err)
	}
	// Projection only mirrors namespaced resources. Cluster-scoped Kinds
	// (Namespace, ClusterRole, StorageClass, CRDs, PriorityClass, ...)
	// either don't make sense to mirror (there can only be one Namespace
	// with a given name in a cluster) or would need a different code path
	// (dynamic-client .Namespace() on a cluster-scoped resource produces
	// a nonsensical URL that the apiserver 404s on). Fail fast with a
	// clear message rather than surfacing the 404 downstream.
	if mapping.Scope.Name() != apimeta.RESTScopeNameNamespace {
		return schema.GroupVersionResource{}, "", fmt.Errorf(
			"%s/%s is cluster-scoped; projection only mirrors namespaced resources",
			src.APIVersion, src.Kind)
	}
	return mapping.Resource, mapping.GroupVersionKind.Version, nil
}

func (r *ProjectionReconciler) deleteDestination(ctx context.Context, proj *projectionv1.Projection) error {
	gvr, _, err := r.resolveGVR(proj.Spec.Source)
	if err != nil {
		// Source Kind no longer resolves — we can't locate the destination.
		// Proceed with finalizer removal rather than blocking forever.
		return nil
	}

	// For selector-based Projections (or any Projection that may have previously
	// used a selector), scan all namespaces for owned destinations.
	if proj.Spec.Destination.NamespaceSelector != nil {
		return r.deleteAllOwnedDestinations(ctx, proj, gvr)
	}

	// Single-namespace path.
	ns, name := destinationCoords(proj)
	return r.deleteOneDestination(ctx, proj, gvr, ns, name)
}

// deleteOneDestination removes a single destination if it's owned by this Projection.
func (r *ProjectionReconciler) deleteOneDestination(ctx context.Context, proj *projectionv1.Projection, gvr schema.GroupVersionResource, ns, name string) error {
	existing, err := r.DynamicClient.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isOwnedBy(existing, proj) {
		r.emit(proj, corev1.EventTypeNormal, "DestinationLeftAlone", "Delete",
			fmt.Sprintf("%s/%s (not owned)", ns, name))
		return nil
	}
	err = r.DynamicClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	r.emit(proj, corev1.EventTypeNormal, "DestinationDeleted", "Delete",
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
func (r *ProjectionReconciler) deleteAllOwnedDestinations(ctx context.Context, proj *projectionv1.Projection, gvr schema.GroupVersionResource) error {
	ownerValue := ownerKey(proj)

	owned, err := r.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{
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
		if err := r.DynamicClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		r.emit(proj, corev1.EventTypeNormal, "DestinationDeleted", "Delete",
			fmt.Sprintf("%s/%s", ns, name))
	}
	return firstErr
}

// handleSourceFetchError funnels errors from the source-object Get call. A
// 404 is a distinct signal ("source has been deleted"): we clean up every
// owned destination (single or selector-based fan-out) and surface
// SourceResolved=False reason=SourceDeleted via failSource, which emits a
// single Warning SourceDeleted event (matches the SourceOptedOut /
// SourceNotProjectable opt-out precedent). Cleanup errors are logged but do
// not block the status update — same opt-out cleanup pattern used when a
// source stops being projectable. All other errors (transient connectivity,
// RBAC blips, 5xx) keep the SourceFetchFailed behavior and leave
// destinations in place.
func (r *ProjectionReconciler) handleSourceFetchError(ctx context.Context, proj *projectionv1.Projection, gvr schema.GroupVersionResource, err error) (ctrl.Result, error) {
	if !apierrors.IsNotFound(err) {
		return r.failSource(ctx, proj, "SourceFetchFailed", "Get", err.Error())
	}
	logger := log.FromContext(ctx)
	if cleanupErr := r.deleteAllOwnedDestinations(ctx, proj, gvr); cleanupErr != nil {
		logger.Error(cleanupErr, "cleaning up destinations after source deletion")
	}
	return r.failSource(ctx, proj, "SourceDeleted", "Get",
		fmt.Sprintf("source %s/%s has been deleted",
			proj.Spec.Source.Namespace, proj.Spec.Source.Name))
}

// resolveDestinationNamespaces returns the list of namespace names the
// destination should be written to. For single-namespace Projections this
// returns a single entry; for selector-based Projections it lists all
// namespaces matching the label selector.
func (r *ProjectionReconciler) resolveDestinationNamespaces(ctx context.Context, proj *projectionv1.Projection) ([]string, error) {
	if proj.Spec.Destination.NamespaceSelector == nil {
		ns, _ := destinationCoords(proj)
		return []string{ns}, nil
	}
	sel, err := metav1.LabelSelectorAsSelector(proj.Spec.Destination.NamespaceSelector)
	if err != nil {
		return nil, fmt.Errorf("parsing namespaceSelector: %w", err)
	}
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}
	result := make([]string, 0, len(nsList.Items))
	for i := range nsList.Items {
		result = append(result, nsList.Items[i].Name)
	}
	return result, nil
}

// cleanupStaleDestinations removes destinations in namespaces that are owned
// by this Projection but are no longer in the current resolved set. This
// handles the case where a namespace loses a label or the selector changes.
// O(owned destinations) per call via a single cluster-wide List filtered by
// the ownedByUIDLabel — independent of total cluster namespace count (#33).
// Runs only for selector-based Projections.
func (r *ProjectionReconciler) cleanupStaleDestinations(ctx context.Context, proj *projectionv1.Projection, gvr schema.GroupVersionResource, currentSet map[string]bool) error {
	// Only needed for selector-based Projections.
	if proj.Spec.Destination.NamespaceSelector == nil {
		return nil
	}
	ownerValue := ownerKey(proj)

	owned, err := r.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{
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
		if err := r.DynamicClient.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		r.emit(proj, corev1.EventTypeNormal, "StaleDestinationDeleted", "Delete",
			fmt.Sprintf("%s/%s", ns, name))
	}
	return firstErr
}

func destinationCoords(proj *projectionv1.Projection) (namespace, name string) {
	namespace = proj.Spec.Destination.Namespace
	if namespace == "" {
		namespace = proj.Namespace
	}
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

// checkSourceProjectable decides whether a freshly-fetched source object is
// allowed to be projected, based on the configured SourceMode and the
// source's projectable annotation. The annotation is evaluated *before* the
// mode, so a hard "false" veto by the source owner is honored under every
// mode.
//
//   - Annotation = "false" is always a veto, regardless of mode (escape
//     hatch — short-circuits before the mode check below).
//   - Annotation = "true" always allows projection.
//   - Anything else (missing, empty, other string): blocked under
//     SourceModeAllowlist (the default), allowed under SourceModePermissive.
//
// Returns (reason, message, ok). When ok is false, the caller should treat
// this as a SourceResolved=False condition with the returned reason and
// message; reason matches the expected scorecard/status vocabulary so
// external tooling can filter.
func (r *ProjectionReconciler) checkSourceProjectable(source *unstructured.Unstructured) (reason, message string, ok bool) {
	val, hasAnnotation := source.GetAnnotations()[projectableAnnotation]

	if hasAnnotation && val == "false" {
		return "SourceOptedOut",
			fmt.Sprintf("source %s/%s has %s=\"false\"; owner has opted out of projection",
				source.GetNamespace(), source.GetName(), projectableAnnotation),
			false
	}

	mode := r.SourceMode
	if mode == "" {
		mode = SourceModeAllowlist
	}
	if mode == SourceModeAllowlist && val != "true" {
		return "SourceNotProjectable",
			fmt.Sprintf("source-mode=allowlist requires annotation %s=\"true\" on source %s/%s",
				projectableAnnotation, source.GetNamespace(), source.GetName()),
			false
	}
	return "", "", true
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

// setCondition mutates proj.Status.Conditions locally (no API call) so callers
// can stage several condition updates and flush them with a single Status().Update.
func setCondition(proj *projectionv1.Projection, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&proj.Status.Conditions, metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

// failSource records a failure that happened before we got a resolved source
// object: either RESTMapper/GVR resolution or the dynamic Get on the source.
// SourceResolved flips to False; DestinationWritten is recorded as Unknown
// because we never got far enough to attempt a write. Ready mirrors the
// source-side reason.
func (r *ProjectionReconciler) failSource(ctx context.Context, proj *projectionv1.Projection, reason, action, msg string) (ctrl.Result, error) {
	setCondition(proj, conditionSourceResolved, metav1.ConditionFalse, reason, msg)
	setCondition(proj, conditionDestinationWritten, metav1.ConditionUnknown, "SourceNotResolved",
		"destination write not attempted because source resolution failed")
	setCondition(proj, conditionReady, metav1.ConditionFalse, reason, msg)
	r.emit(proj, corev1.EventTypeWarning, reason, action, msg)
	reconcileTotal.WithLabelValues(resultSourceError).Inc()
	if err := r.Status().Update(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

// failDestination records a failure that happened during the write stage. By
// the time we get here we've already fetched the source, so SourceResolved is
// True; DestinationWritten flips to False. Ready mirrors the destination-side
// reason. Callers are responsible for emitting the event — failDestination
// only touches status, conditions, and metrics. This keeps the rollup path
// (which has already fired per-namespace events in the fan-out loop) from
// double-emitting, which the client-go events broadcaster would otherwise
// aggregate into a record that drops the action field.
func (r *ProjectionReconciler) failDestination(ctx context.Context, proj *projectionv1.Projection, resolvedVersion, reason, msg string) (ctrl.Result, error) {
	srMsg := resolvedVersionMessage(proj.Spec.Source, resolvedVersion)
	setCondition(proj, conditionSourceResolved, metav1.ConditionTrue, "Resolved", srMsg)
	setCondition(proj, conditionDestinationWritten, metav1.ConditionFalse, reason, msg)
	setCondition(proj, conditionReady, metav1.ConditionFalse, reason, msg)
	switch reason {
	case "DestinationConflict":
		reconcileTotal.WithLabelValues(resultConflict).Inc()
	default:
		reconcileTotal.WithLabelValues(resultDestinationError).Inc()
	}
	if err := r.Status().Update(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

// markAllReady flips all three conditions to True in a single status update.
func (r *ProjectionReconciler) markAllReady(ctx context.Context, proj *projectionv1.Projection, resolvedVersion string) error {
	msg := resolvedVersionMessage(proj.Spec.Source, resolvedVersion)
	setCondition(proj, conditionSourceResolved, metav1.ConditionTrue, "Resolved", msg)
	setCondition(proj, conditionDestinationWritten, metav1.ConditionTrue, "Projected", "")
	setCondition(proj, conditionReady, metav1.ConditionTrue, "Projected", "")
	if err := r.Status().Update(ctx, proj); err != nil {
		return err
	}
	reconcileTotal.WithLabelValues(resultSuccess).Inc()
	return nil
}

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

// mapNamespace maps a Namespace event to reconcile requests for all
// Projections that use a namespaceSelector. When a namespace is
// created/updated/deleted, the matching set for selector-based Projections
// may have changed.
func (r *ProjectionReconciler) mapNamespace(ctx context.Context, _ client.Object) []reconcile.Request {
	var list projectionv1.ProjectionList
	if err := r.List(ctx, &list, client.MatchingFields{selectorIndex: selectorIndexValue}); err != nil {
		log.FromContext(ctx).Error(err, "listing selector-based projections for namespace event")
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
