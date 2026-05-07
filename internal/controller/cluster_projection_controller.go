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
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// ClusterProjectionReconciler reconciles cluster-scoped ClusterProjection
// objects: a single source object is fanned out to many destination
// namespaces selected either by an explicit list or a label selector.
//
// State separated from ProjectionReconciler because the two reconcilers
// must not contend on the same source-watch and dest-watch maps —
// distinct ownership tiers, distinct enqueue paths, distinct cleanup
// semantics. Apiserver-facing dependencies are shared via *ControllerDeps.
type ClusterProjectionReconciler struct {
	*ControllerDeps

	// SourceMode is the cluster-admin-configured policy for which source
	// objects are projectable. Empty string defaults to SourceModeAllowlist.
	// Mirrors the namespaced reconciler's flag — both honor the same
	// projectable-annotation veto and the same allowlist/permissive modes.
	SourceMode SourceMode

	// RequeueInterval controls how long the reconciler sleeps before
	// retrying after a failed reconcile. Defaults to 30 seconds when unset
	// (filled by SetupWithManager).
	RequeueInterval time.Duration

	// SelectorWriteConcurrency caps the number of in-flight destination
	// writes during selector-based fan-out. Bounded so a CR matching
	// thousands of namespaces doesn't burst the apiserver. Defaults to
	// 16 when SetupWithManager runs against a zero value.
	SelectorWriteConcurrency int

	// Controller and Cache are captured by SetupWithManager so Reconcile
	// can lazily register source / destination watches as new GVKs appear.
	// Both are nil in unit-test paths that call Reconcile directly without
	// SetupWithManager — the watch helpers no-op in that mode.
	Controller controller.Controller
	Cache      cache.Cache

	// Source-watch state. Same shape as ProjectionReconciler.watched: one
	// entry per resolved source GVK, idempotent insertion.
	watchedMu sync.Mutex
	watched   map[schema.GroupVersionKind]bool

	// Destination-watch state. ensureDestWatch deduplicates so we register
	// at most one watch per dest GVK regardless of how many
	// ClusterProjections converge on the same Kind.
	watchedDestsMu sync.Mutex
	watchedDests   map[schema.GroupVersionKind]bool
}

// Field-indexer key for cluster-projection source lookups. Shares the
// sourceKey shape with the namespaced reconciler so the same canonical
// string format works on both indexers.
const clusterSourceIndex = "spec.sourceKey"

// +kubebuilder:rbac:groups=projection.sh,resources=clusterprojections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=projection.sh,resources=clusterprojections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=projection.sh,resources=clusterprojections/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// ClusterProjection can mirror any Kind, so the controller needs broad access.
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete

func (r *ClusterProjectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	cp := &projectionv1.ClusterProjection{}
	if err := r.Get(ctx, req.NamespacedName, cp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if handled, err := r.handleClusterDeletion(ctx, cp); handled {
		return ctrl.Result{}, err
	}

	if err := r.ensureClusterFinalizer(ctx, cp); err != nil {
		return ctrl.Result{}, err
	}

	gvr, resolvedVersion, err := r.resolveGVR(cp.Spec.Source)
	if err != nil {
		return r.failClusterSource(ctx, cp, "SourceResolutionFailed", "Resolve", err.Error())
	}

	watchGVK := schema.GroupVersionKind{
		Group:   gvr.Group,
		Version: resolvedVersion,
		Kind:    cp.Spec.Source.Kind,
	}
	if err := r.ensureSourceWatch(watchGVK); err != nil {
		logger.Error(err, "registering source watch", "gvk", watchGVK)
	}

	src, err := r.DynamicClient.Resource(gvr).
		Namespace(cp.Spec.Source.Namespace).
		Get(ctx, cp.Spec.Source.Name, metav1.GetOptions{})
	if err != nil {
		return r.handleClusterSourceFetchError(ctx, cp, gvr, err)
	}

	if reason, msg, ok := r.checkSourceProjectable(src); !ok {
		// Source isn't projectable — clean up every owned destination
		// across all targeted namespaces. Mirrors the namespaced
		// reconciler's opt-out cleanup behavior.
		if delErr := r.deleteAllClusterOwnedDestinations(ctx, cp, gvr); delErr != nil {
			logger.Error(delErr, "cleaning up cluster destinations after opt-out", "reason", reason)
		}
		return r.failClusterSource(ctx, cp, reason, "Validate", msg)
	}

	targets, targetErr := r.resolveTargetNamespaces(ctx, cp)
	if targetErr != nil {
		return r.failClusterDestination(ctx, cp, resolvedVersion, "TargetResolutionFailed", targetErr.Error())
	}

	written, failed, failureMsgs := r.fanOut(ctx, cp, src, targets)

	// Sweep stale destinations whose namespace is no longer in the target
	// set. Best-effort: a list error is logged but doesn't block the
	// status rollup — the next reconcile will retry.
	if cleanupErr := r.cleanupStaleClusterDestinations(ctx, cp, gvr, targets); cleanupErr != nil {
		logger.Error(cleanupErr, "cleaning up stale cluster destinations")
	}

	// Register the dest-side watch after the fan-out completes (whether or
	// not any writes succeeded) so a subsequent manual `kubectl delete` of
	// a projected object self-heals without waiting for the periodic
	// resync. Registering on partial-failure too is fine: the watch is
	// keyed on dest GVK, idempotent, and only fires for objects we own.
	if err := r.ensureClusterDestWatch(watchGVK); err != nil {
		logger.Error(err, "registering destination watch", "gvk", watchGVK)
	}

	if failed > 0 {
		// Surface a single DestinationWritten=False rollup that names a
		// few of the failed namespaces; status.NamespacesFailed carries
		// the full count.
		return r.failClusterDestinationCounts(ctx, cp, resolvedVersion,
			"DestinationWriteFailed", summarizeFailures(failed, failureMsgs),
			int32(written), int32(failed))
	}

	if err := r.markAllClusterReady(ctx, cp, resolvedVersion, int32(written)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// resolveTargetNamespaces returns the resolved set of destination namespaces
// for cp, in deterministic order. Either reads spec.destination.namespaces
// verbatim (explicit-list path) or runs a cached LIST of namespaces with
// the configured selector applied (selector path).
func (r *ClusterProjectionReconciler) resolveTargetNamespaces(ctx context.Context, cp *projectionv1.ClusterProjection) ([]string, error) {
	if len(cp.Spec.Destination.Namespaces) > 0 {
		// Explicit list — already deterministic via the user's spec
		// authoring order, but normalize so status counts and event
		// strings don't depend on the YAML key insertion order.
		out := append([]string(nil), cp.Spec.Destination.Namespaces...)
		sort.Strings(out)
		return out, nil
	}
	if cp.Spec.Destination.NamespaceSelector == nil {
		// Admission CEL forbids this, but we belt-and-braces here so a
		// hand-edited stale CR doesn't crash the reconciler.
		return nil, fmt.Errorf("ClusterProjection has neither destination.namespaces nor destination.namespaceSelector")
	}
	sel, err := metav1.LabelSelectorAsSelector(cp.Spec.Destination.NamespaceSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid namespaceSelector: %w", err)
	}
	if sel.Empty() {
		// An empty selector matches all namespaces. Reject to avoid an
		// accidental "fan out into every namespace" footgun — the user
		// can opt in by listing namespaces explicitly.
		return nil, fmt.Errorf("namespaceSelector matches all namespaces; refusing fan-out across the entire cluster")
	}
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, &client.ListOptions{LabelSelector: sel}); err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}
	out := make([]string, 0, len(nsList.Items))
	for i := range nsList.Items {
		// Skip namespaces that are terminating — writing into them is a
		// no-op at best, an apiserver 403 at worst.
		if nsList.Items[i].DeletionTimestamp != nil {
			continue
		}
		out = append(out, nsList.Items[i].Name)
	}
	sort.Strings(out)
	return out, nil
}

// fanOut performs the per-namespace destination write across the given
// target set with a worker pool capped at SelectorWriteConcurrency.
// Returns (written, failed, failureMessages-by-namespace) for status
// rollup. Errors do not cause the function to return early — every
// target gets its own attempt so a single transient apiserver hiccup in
// one namespace doesn't starve the rest.
func (r *ClusterProjectionReconciler) fanOut(
	ctx context.Context,
	cp *projectionv1.ClusterProjection,
	src *unstructured.Unstructured,
	targets []string,
) (written, failed int, failureMsgs map[string]string) {
	failureMsgs = map[string]string{}
	if len(targets) == 0 {
		return 0, 0, failureMsgs
	}

	concurrency := r.SelectorWriteConcurrency
	if concurrency <= 0 {
		concurrency = 16
	}
	if concurrency > len(targets) {
		concurrency = len(targets)
	}

	type result struct {
		ns     string
		reason string
		msg    string
	}
	jobs := make(chan string, len(targets))
	results := make(chan result, len(targets))

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ns := range jobs {
				reason, msg := r.writeClusterDestination(ctx, cp, src, ns)
				results <- result{ns: ns, reason: reason, msg: msg}
			}
		}()
	}
	for _, ns := range targets {
		jobs <- ns
	}
	close(jobs)
	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		if res.reason == "" {
			written++
			continue
		}
		failed++
		failureMsgs[res.ns] = fmt.Sprintf("%s: %s", res.reason, res.msg)
	}
	return written, failed, failureMsgs
}

// summarizeFailures renders a (truncated) human-readable summary of
// failed namespaces for the DestinationWritten condition message. Caps
// the per-message namespace count so a 5000-namespace CR with a wedged
// apiserver doesn't generate a multi-MB condition.
func summarizeFailures(total int, failureMsgs map[string]string) string {
	const maxNamespacesInMessage = 5
	names := make([]string, 0, len(failureMsgs))
	for ns := range failureMsgs {
		names = append(names, ns)
	}
	sort.Strings(names)
	shown := names
	if len(shown) > maxNamespacesInMessage {
		shown = shown[:maxNamespacesInMessage]
	}
	if total > len(shown) {
		return fmt.Sprintf("write failed in %d namespaces (first: %v, +%d more)",
			total, shown, total-len(shown))
	}
	return fmt.Sprintf("write failed in %d namespaces: %v", total, shown)
}

// handleClusterSourceFetchError funnels source-fetch errors. A 404
// cleans up every owned destination across all namespaces and surfaces
// SourceResolved=False; other errors keep the existing destinations in
// place and surface SourceFetchFailed. The 404 reason distinguishes two
// cases by status.destinationName:
//   - empty: never resolved — reason=SourceNotFound, "source X/Y not found".
//   - populated: previously projected, destination was named —
//     reason=SourceDeleted, "source X/Y has been deleted".
func (r *ClusterProjectionReconciler) handleClusterSourceFetchError(
	ctx context.Context,
	cp *projectionv1.ClusterProjection,
	gvr schema.GroupVersionResource,
	err error,
) (ctrl.Result, error) {
	if !apierrors.IsNotFound(err) {
		return r.failClusterSource(ctx, cp, "SourceFetchFailed", "Get", err.Error())
	}
	logger := log.FromContext(ctx)
	if cleanupErr := r.deleteAllClusterOwnedDestinations(ctx, cp, gvr); cleanupErr != nil {
		logger.Error(cleanupErr, "cleaning up cluster destinations after source deletion")
	}
	reason := "SourceNotFound"
	msg := fmt.Sprintf("source %s/%s not found", cp.Spec.Source.Namespace, cp.Spec.Source.Name)
	if cp.Status.DestinationName != "" {
		reason = "SourceDeleted"
		msg = fmt.Sprintf("source %s/%s has been deleted", cp.Spec.Source.Namespace, cp.Spec.Source.Name)
	}
	return r.failClusterSource(ctx, cp, reason, "Get", msg)
}

// ensureSourceWatch is the cluster-tier sibling of the namespaced version.
// Registers a dynamic source watch keyed on the resolved GVK so a source
// edit enqueues every ClusterProjection pointing at that source via the
// clusterSourceIndex field indexer.
func (r *ClusterProjectionReconciler) ensureSourceWatch(gvk schema.GroupVersionKind) error {
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
// every ClusterProjection that references it.
func (r *ClusterProjectionReconciler) mapSource(ctx context.Context, obj client.Object) []reconcile.Request {
	gvk := obj.GetObjectKind().GroupVersionKind()
	key := sourceKey(projectionv1.SourceRef{
		Group:     gvk.Group,
		Kind:      gvk.Kind,
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	})
	var list projectionv1.ClusterProjectionList
	if err := r.List(ctx, &list, client.MatchingFields{clusterSourceIndex: key}); err != nil {
		log.FromContext(ctx).Error(err, "listing cluster-projections by source index", "key", key)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: client.ObjectKey{Name: list.Items[i].Name},
		})
	}
	return reqs
}

// ensureClusterDestWatch wires the shared ensureDestWatch helper for the
// cluster-tier UID label and field indexer.
func (r *ClusterProjectionReconciler) ensureClusterDestWatch(gvk schema.GroupVersionKind) error {
	if r.watchedDests == nil {
		// Defensive: SetupWithManager initializes this, but unit-test
		// paths that call Reconcile directly without SetupWithManager
		// have a zero-value map. The shared ensureDestWatch will no-op
		// when Controller/Cache are nil; this keeps the lookup safe.
		r.watchedDestsMu.Lock()
		if r.watchedDests == nil {
			r.watchedDests = map[schema.GroupVersionKind]bool{}
		}
		r.watchedDestsMu.Unlock()
	}
	return r.ensureDestWatch(r.Controller, r.Cache, gvk, ownedByClusterUIDLabel,
		r.enqueueByUID, r.watchedDests, &r.watchedDestsMu)
}

// enqueueByUID resolves a UID-label value back to the owning
// ClusterProjection's namespaced name (cluster-scoped, so namespace is
// empty) via the metadata.uid field indexer.
func (r *ClusterProjectionReconciler) enqueueByUID(ctx context.Context, uid string) []reconcile.Request {
	if uid == "" {
		return nil
	}
	var list projectionv1.ClusterProjectionList
	if err := r.List(ctx, &list, client.MatchingFields{uidIndex: uid}); err != nil {
		log.FromContext(ctx).Error(err, "listing cluster-projections by uid index", "uid", uid)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: client.ObjectKey{Name: list.Items[i].Name},
		})
	}
	return reqs
}

// mapNamespace translates a Namespace event into reconcile.Requests for
// every ClusterProjection whose selector matches the namespace's labels.
// Re-enqueues fan-out work when a namespace gains or loses a selector
// label without waiting for the periodic resync.
func (r *ClusterProjectionReconciler) mapNamespace(ctx context.Context, obj client.Object) []reconcile.Request {
	eventNS := obj.GetName()
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		// PartialObjectMetadata or similar; we still match labels.
		var list projectionv1.ClusterProjectionList
		if err := r.List(ctx, &list); err != nil {
			return nil
		}
		return r.matchingClusterProjectionRequests(list.Items, eventNS, obj.GetLabels())
	}
	var list projectionv1.ClusterProjectionList
	if err := r.List(ctx, &list); err != nil {
		log.FromContext(ctx).Error(err, "listing cluster-projections for namespace event", "namespace", ns.Name)
		return nil
	}
	return r.matchingClusterProjectionRequests(list.Items, eventNS, ns.Labels)
}

// matchingClusterProjectionRequests returns the reconcile.Requests for
// every ClusterProjection in items that should be re-enqueued in response
// to a namespace event for eventNS (with labels nsLabels).
//
// For explicit-list CPs we only enqueue when eventNS is in the list — a
// previous version enqueued every explicit-list CP regardless, which was a
// soft-DoS during namespace churn (N CPs, each unrelated to the event,
// still got re-reconciled). For selector CPs we enqueue whenever the
// selector matches nsLabels — the selector might newly match (or stop
// matching, requiring stale-cleanup), so we can't filter by current
// membership.
//
// The list-then-filter shape avoids needing a per-(label-key) field
// indexer — at the cluster-projection scale we expect (tens, not
// thousands), the filter is trivial.
func (r *ClusterProjectionReconciler) matchingClusterProjectionRequests(items []projectionv1.ClusterProjection, eventNS string, nsLabels map[string]string) []reconcile.Request {
	var reqs []reconcile.Request
	for i := range items {
		cp := &items[i]
		// Explicit-list ClusterProjections care about namespace add/delete
		// events for namespaces they actually target. Filter by
		// membership: only enqueue when eventNS appears in the list.
		if len(cp.Spec.Destination.Namespaces) > 0 {
			for _, ns := range cp.Spec.Destination.Namespaces {
				if ns == eventNS {
					reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKey{Name: cp.Name}})
					break
				}
			}
			continue
		}
		if cp.Spec.Destination.NamespaceSelector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(cp.Spec.Destination.NamespaceSelector)
		if err != nil {
			continue
		}
		if sel.Empty() {
			// Empty selector is rejected at resolveTargetNamespaces
			// time; don't re-enqueue work for it on namespace events.
			continue
		}
		if sel.Matches(labels.Set(nsLabels)) {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKey{Name: cp.Name}})
		}
	}
	return reqs
}

// failClusterSource records a source-side failure. Mirrors the namespaced
// failSource behavior — DestinationWritten flips to Unknown because we
// never reached the write stage.
func (r *ClusterProjectionReconciler) failClusterSource(ctx context.Context, cp *projectionv1.ClusterProjection, reason, action, msg string) (ctrl.Result, error) {
	setClusterCondition(cp, conditionSourceResolved, metav1.ConditionFalse, reason, msg)
	setClusterCondition(cp, conditionDestinationWritten, metav1.ConditionUnknown, "SourceNotResolved",
		"destination write not attempted because source resolution failed")
	setClusterCondition(cp, conditionReady, metav1.ConditionFalse, reason, msg)
	r.emit(cp, corev1.EventTypeWarning, reason, action, msg)
	reconcileTotal.WithLabelValues(kindClusterProjection, resultSourceError).Inc()
	if err := r.Status().Update(ctx, cp); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

// failClusterDestination records a generic destination-write failure
// before fan-out — typically a target-resolution error. Status counts
// are zeroed because nothing was written.
func (r *ClusterProjectionReconciler) failClusterDestination(ctx context.Context, cp *projectionv1.ClusterProjection, resolvedVersion, reason, msg string) (ctrl.Result, error) {
	srMsg := resolvedClusterVersionMessage(cp.Spec.Source, resolvedVersion)
	setClusterCondition(cp, conditionSourceResolved, metav1.ConditionTrue, "Resolved", srMsg)
	setClusterCondition(cp, conditionDestinationWritten, metav1.ConditionFalse, reason, msg)
	setClusterCondition(cp, conditionReady, metav1.ConditionFalse, reason, msg)
	cp.Status.NamespacesWritten = 0
	cp.Status.NamespacesFailed = 0
	// Today the only call site passes reason="TargetResolutionFailed"; the
	// per-namespace conflict path goes through failClusterDestinationCounts
	// instead. Bucket every reason into resultDestinationError so the
	// metric stays accurate without a branch that's never exercised.
	reconcileTotal.WithLabelValues(kindClusterProjection, resultDestinationError).Inc()
	if err := r.Status().Update(ctx, cp); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

// failClusterDestinationCounts is the post-fan-out failure path. Carries
// the resolved per-namespace counts so status correctly reflects partial
// success.
func (r *ClusterProjectionReconciler) failClusterDestinationCounts(
	ctx context.Context,
	cp *projectionv1.ClusterProjection,
	resolvedVersion, reason, msg string,
	written, failed int32,
) (ctrl.Result, error) {
	srMsg := resolvedClusterVersionMessage(cp.Spec.Source, resolvedVersion)
	setClusterCondition(cp, conditionSourceResolved, metav1.ConditionTrue, "Resolved", srMsg)
	setClusterCondition(cp, conditionDestinationWritten, metav1.ConditionFalse, reason, msg)
	setClusterCondition(cp, conditionReady, metav1.ConditionFalse, reason, msg)
	cp.Status.DestinationName = clusterDestinationName(cp)
	cp.Status.NamespacesWritten = written
	cp.Status.NamespacesFailed = failed
	reconcileTotal.WithLabelValues(kindClusterProjection, resultDestinationError).Inc()
	if err := r.Status().Update(ctx, cp); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

// markAllClusterReady stamps the success path: all conditions True,
// destination name resolved, written count populated, failed reset.
func (r *ClusterProjectionReconciler) markAllClusterReady(ctx context.Context, cp *projectionv1.ClusterProjection, resolvedVersion string, written int32) error {
	msg := resolvedClusterVersionMessage(cp.Spec.Source, resolvedVersion)
	setClusterCondition(cp, conditionSourceResolved, metav1.ConditionTrue, "Resolved", msg)
	setClusterCondition(cp, conditionDestinationWritten, metav1.ConditionTrue, "Projected", "")
	setClusterCondition(cp, conditionReady, metav1.ConditionTrue, "Projected", "")
	cp.Status.DestinationName = clusterDestinationName(cp)
	cp.Status.NamespacesWritten = written
	cp.Status.NamespacesFailed = 0
	if err := r.Status().Update(ctx, cp); err != nil {
		return err
	}
	reconcileTotal.WithLabelValues(kindClusterProjection, resultSuccess).Inc()
	return nil
}

// resolvedClusterVersionMessage produces the SourceResolved condition
// message when the user omitted source.version and the RESTMapper picked
// a concrete version. Same shape as the namespaced helper; duplicated
// only because the type-switch boilerplate to share would outweigh the
// duplication.
func resolvedClusterVersionMessage(src projectionv1.SourceRef, resolvedVersion string) string {
	if src.Version != "" || resolvedVersion == "" {
		return ""
	}
	return fmt.Sprintf("resolved %s/%s to preferred version %s",
		src.Group, src.Kind, resolvedVersion)
}

// SetupWithManager wires the cluster reconciler to the manager.
//
// Three things happen here that Reconcile relies on:
//
//  1. A field indexer on ClusterProjection.spec indexes each CR by the
//     canonical sourceKey of its source ref (mapSource resolves source
//     events through this).
//  2. A field indexer on ClusterProjection.metadata.uid indexes each CR
//     by UID (ensureClusterDestWatch resolves dest events through this
//     in O(1)).
//  3. A namespace watch is registered on the builder so a namespace
//     gaining or losing a selector label re-enqueues every affected
//     ClusterProjection — without this, the only catch-up path would be
//     the periodic resync.
//
// We use .Build(r) (not .Complete(r)) to capture the controller.Controller
// so Reconcile can lazily register source / destination watches.
func (r *ClusterProjectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.RequeueInterval == 0 {
		r.RequeueInterval = 30 * time.Second
	}
	if r.SelectorWriteConcurrency <= 0 {
		r.SelectorWriteConcurrency = 16
	}
	r.watchedDests = map[schema.GroupVersionKind]bool{}
	r.watched = map[schema.GroupVersionKind]bool{}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &projectionv1.ClusterProjection{}, clusterSourceIndex,
		func(obj client.Object) []string {
			cp, ok := obj.(*projectionv1.ClusterProjection)
			if !ok {
				return nil
			}
			return []string{sourceKey(cp.Spec.Source)}
		}); err != nil {
		return fmt.Errorf("registering cluster-projection source field indexer: %w", err)
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &projectionv1.ClusterProjection{}, uidIndex,
		func(obj client.Object) []string {
			cp, ok := obj.(*projectionv1.ClusterProjection)
			if !ok {
				return nil
			}
			return []string{string(cp.UID)}
		}); err != nil {
		return fmt.Errorf("registering cluster-projection uid field indexer: %w", err)
	}

	c, err := builder.ControllerManagedBy(mgr).
		// Distinct controller name from the namespaced reconciler so a
		// single manager can run both without colliding on the
		// controller-name validation in controller-runtime (which guards
		// against duplicate metric labels).
		Named("clusterprojection").
		For(&projectionv1.ClusterProjection{}).
		WatchesMetadata(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.mapNamespace),
		).
		Build(r)
	if err != nil {
		return err
	}
	r.Controller = c
	r.Cache = mgr.GetCache()
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorder("cluster-projection-controller")
	}
	return nil
}
