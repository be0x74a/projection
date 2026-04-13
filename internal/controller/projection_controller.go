/*
Copyright 2024.

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
	"k8s.io/client-go/tools/record"
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

	projectionv1 "github.com/be0x74a/projection/api/v1"
)

const (
	finalizerName     = "projection.be0x74a.io/finalizer"
	ownedByAnnotation = "projection.be0x74a.io/owned-by"
	requeueInterval   = 30 * time.Second

	conditionReady              = "Ready"
	conditionSourceResolved     = "SourceResolved"
	conditionDestinationWritten = "DestinationWritten"

	// sourceIndex is the field-indexer key we register on Projection so that
	// a source-object event can be mapped to all Projections pointing at it
	// via a single cached List(MatchingFields).
	sourceIndex = "spec.sourceKey"
)

// ProjectionReconciler reconciles a Projection object.
type ProjectionReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	DynamicClient dynamic.Interface
	RESTMapper    apimeta.RESTMapper
	Recorder      record.EventRecorder

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
// need to plumb a recorder.
func (r *ProjectionReconciler) emit(proj *projectionv1.Projection, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(proj, eventType, reason, message)
}

// sourceKey is the canonical string key identifying a source object across
// both the field indexer and the event-mapping function. Keeping one helper
// ensures the two sides can never drift.
func sourceKey(apiVersion, kind, namespace, name string) string {
	return apiVersion + "/" + kind + "/" + namespace + "/" + name
}

// +kubebuilder:rbac:groups=projection.be0x74a.io,resources=projections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=projection.be0x74a.io,resources=projections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=projection.be0x74a.io,resources=projections/finalizers,verbs=update
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
		return ctrl.Result{Requeue: true}, nil
	}

	gvr, err := r.resolveGVR(proj.Spec.Source)
	if err != nil {
		return r.failSource(ctx, proj, "SourceResolutionFailed", err.Error())
	}

	// Register a watch on this source GVK if we haven't seen it before, so
	// subsequent edits to the source enqueue this (and any other) Projection
	// pointing at it instead of waiting for the next periodic requeue.
	gv, _ := schema.ParseGroupVersion(proj.Spec.Source.APIVersion)
	if err := r.ensureWatch(gv.WithKind(proj.Spec.Source.Kind)); err != nil {
		logger.Error(err, "registering source watch", "gvk", gv.WithKind(proj.Spec.Source.Kind))
		// Don't fail the reconcile: the periodic-requeue-on-error path below
		// still keeps us alive, and a future reconcile will retry the watch.
	}

	source, err := r.DynamicClient.Resource(gvr).
		Namespace(proj.Spec.Source.Namespace).
		Get(ctx, proj.Spec.Source.Name, metav1.GetOptions{})
	if err != nil {
		return r.failSource(ctx, proj, "SourceFetchFailed", err.Error())
	}

	dst := buildDestination(source, proj)

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(source.GroupVersionKind())
	key := client.ObjectKey{Namespace: dst.GetNamespace(), Name: dst.GetName()}
	switch err := r.Get(ctx, key, existing); {
	case apierrors.IsNotFound(err):
		if err := r.Create(ctx, dst); err != nil {
			return r.failDestination(ctx, proj, "DestinationCreateFailed", err.Error())
		}
		r.emit(proj, corev1.EventTypeNormal, "Projected",
			fmt.Sprintf("projected %s %s/%s to %s/%s",
				source.GroupVersionKind().String(),
				proj.Spec.Source.Namespace, proj.Spec.Source.Name,
				dst.GetNamespace(), dst.GetName()))
	case err != nil:
		return r.failDestination(ctx, proj, "DestinationFetchFailed", err.Error())
	default:
		if !isOwnedBy(existing, proj) {
			msg := fmt.Sprintf("destination %s/%s exists and is not owned by this Projection",
				key.Namespace, key.Name)
			return r.failDestination(ctx, proj, "DestinationConflict", msg)
		}
		// Carry over fields the apiserver allocated on create (clusterIP,
		// volumeName, ...) — if we sent dst as-is, an Update of e.g. a Service
		// would be rejected for trying to clear an immutable field.
		preserveAPIServerAllocatedFields(existing, dst)
		if !needsUpdate(existing, dst) {
			// Destination already matches desired state. Skip the Update so
			// we don't generate a noisy "Updated" event/metric on every
			// reconcile of an unchanged Projection.
			break
		}
		dst.SetResourceVersion(existing.GetResourceVersion())
		if err := r.Update(ctx, dst); err != nil {
			return r.failDestination(ctx, proj, "DestinationUpdateFailed", err.Error())
		}
		r.emit(proj, corev1.EventTypeNormal, "Updated",
			fmt.Sprintf("updated %s %s/%s to %s/%s",
				source.GroupVersionKind().String(),
				proj.Spec.Source.Namespace, proj.Spec.Source.Name,
				dst.GetNamespace(), dst.GetName()))
	}

	if err := r.markAllReady(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}

	// No periodic requeue: the dynamic source watch registered above is
	// authoritative for propagating future source edits.
	return ctrl.Result{}, nil
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
	return nil
}

// mapSource translates a source-object event into reconcile.Requests for
// every Projection that references it. The field indexer keyed on sourceKey
// lets us do this with a single cached List.
func (r *ProjectionReconciler) mapSource(ctx context.Context, obj client.Object) []reconcile.Request {
	gvk := obj.GetObjectKind().GroupVersionKind()
	key := sourceKey(gvk.GroupVersion().String(), gvk.Kind, obj.GetNamespace(), obj.GetName())
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

func (r *ProjectionReconciler) resolveGVR(src projectionv1.SourceRef) (schema.GroupVersionResource, error) {
	gv, err := schema.ParseGroupVersion(src.APIVersion)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("parsing apiVersion %q: %w", src.APIVersion, err)
	}
	mapping, err := r.RESTMapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: src.Kind}, gv.Version)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("resolving %s/%s: %w", src.APIVersion, src.Kind, err)
	}
	return mapping.Resource, nil
}

func (r *ProjectionReconciler) deleteDestination(ctx context.Context, proj *projectionv1.Projection) error {
	gvr, err := r.resolveGVR(proj.Spec.Source)
	if err != nil {
		// Source Kind no longer resolves — we can't locate the destination.
		// Proceed with finalizer removal rather than blocking forever.
		return nil
	}
	ns, name := destinationCoords(proj)
	existing, err := r.DynamicClient.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !isOwnedBy(existing, proj) {
		r.emit(proj, corev1.EventTypeNormal, "DestinationLeftAlone",
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
	r.emit(proj, corev1.EventTypeNormal, "DestinationDeleted",
		fmt.Sprintf("%s/%s", ns, name))
	return nil
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

func isOwnedBy(obj *unstructured.Unstructured, proj *projectionv1.Projection) bool {
	return obj.GetAnnotations()[ownedByAnnotation] == proj.Namespace+"/"+proj.Name
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
func (r *ProjectionReconciler) failSource(ctx context.Context, proj *projectionv1.Projection, reason, msg string) (ctrl.Result, error) {
	setCondition(proj, conditionSourceResolved, metav1.ConditionFalse, reason, msg)
	setCondition(proj, conditionDestinationWritten, metav1.ConditionUnknown, "SourceNotResolved",
		"destination write not attempted because source resolution failed")
	setCondition(proj, conditionReady, metav1.ConditionFalse, reason, msg)
	r.emit(proj, corev1.EventTypeWarning, reason, msg)
	reconcileTotal.WithLabelValues(resultSourceError).Inc()
	if err := r.Status().Update(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// failDestination records a failure that happened during the write stage. By
// the time we get here we've already fetched the source, so SourceResolved is
// True; DestinationWritten flips to False. Ready mirrors the destination-side
// reason.
func (r *ProjectionReconciler) failDestination(ctx context.Context, proj *projectionv1.Projection, reason, msg string) (ctrl.Result, error) {
	setCondition(proj, conditionSourceResolved, metav1.ConditionTrue, "Resolved", "")
	setCondition(proj, conditionDestinationWritten, metav1.ConditionFalse, reason, msg)
	setCondition(proj, conditionReady, metav1.ConditionFalse, reason, msg)
	r.emit(proj, corev1.EventTypeWarning, reason, msg)
	switch reason {
	case "DestinationConflict":
		reconcileTotal.WithLabelValues(resultConflict).Inc()
	default:
		reconcileTotal.WithLabelValues(resultDestinationError).Inc()
	}
	if err := r.Status().Update(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// markAllReady flips all three conditions to True in a single status update.
func (r *ProjectionReconciler) markAllReady(ctx context.Context, proj *projectionv1.Projection) error {
	setCondition(proj, conditionSourceResolved, metav1.ConditionTrue, "Resolved", "")
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
}

// buildDestination builds the object to write to the destination namespace.
// It preserves the source's spec/data and user-set labels/annotations, drops
// server-owned and cross-namespace-unsafe metadata, and applies the overlay
// on top. The ownership annotation is required: subsequent reconciles use it
// to distinguish our destination from a stranger's and refuse to overwrite
// the latter.
func buildDestination(source *unstructured.Unstructured, proj *projectionv1.Projection) *unstructured.Unstructured {
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
	annotations[ownedByAnnotation] = proj.Namespace + "/" + proj.Name
	dst.SetAnnotations(annotations)

	if len(proj.Spec.Overlay.Labels) > 0 {
		labels := dst.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		for k, v := range proj.Spec.Overlay.Labels {
			labels[k] = v
		}
		dst.SetLabels(labels)
	}

	ns, name := destinationCoords(proj)
	dst.SetNamespace(ns)
	dst.SetName(name)

	return dst
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
func (r *ProjectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &projectionv1.Projection{}, sourceIndex,
		func(obj client.Object) []string {
			p, ok := obj.(*projectionv1.Projection)
			if !ok {
				return nil
			}
			return []string{sourceKey(
				p.Spec.Source.APIVersion,
				p.Spec.Source.Kind,
				p.Spec.Source.Namespace,
				p.Spec.Source.Name,
			)}
		}); err != nil {
		return fmt.Errorf("registering source field indexer: %w", err)
	}

	c, err := builder.ControllerManagedBy(mgr).
		For(&projectionv1.Projection{}).
		Build(r)
	if err != nil {
		return err
	}
	r.Controller = c
	r.Cache = mgr.GetCache()
	// controller-runtime v0.23 deprecated this in favor of GetEventRecorder
	// returning the new events.EventRecorder (k8s.io/client-go/tools/events).
	// Migration requires changing every r.Recorder.Event(...) call to Eventf(...)
	// and re-plumbing FakeRecorder in tests — defer to a focused PR.
	r.Recorder = mgr.GetEventRecorderFor("projection-controller") //nolint:staticcheck // SA1019: legacy record.EventRecorder still works; migration deferred
	return nil
}
