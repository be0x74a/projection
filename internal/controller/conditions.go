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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// Condition types surfaced on the Projection's status.
const (
	conditionReady              = "Ready"
	conditionSourceResolved     = "SourceResolved"
	conditionDestinationWritten = "DestinationWritten"
)

// resolvedVersionMessage produces the human-readable SourceResolved
// condition message when a Projection used the unpinned form (omitting
// source.version) and the RESTMapper picked a concrete version. Returns
// "" for pinned sources to preserve today's empty-message behavior.
func resolvedVersionMessage(src projectionv1.SourceRef, resolvedVersion string) string {
	// resolvedVersion == "" means failDestination was called before
	// resolveGVR ran; there's no version to report yet.
	if src.Version != "" || resolvedVersion == "" {
		return ""
	}
	return fmt.Sprintf("resolved %s/%s to preferred version %s",
		src.Group, src.Kind, resolvedVersion)
}

// setConditionOn mutates the given conditions slice locally (no API call) so
// callers can stage several condition updates and flush them with a single
// Status().Update. CR-agnostic — both Projection and ClusterProjection point
// at this via tiny wrappers.
func setConditionOn(conds *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(conds, metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

// setCondition mutates proj.Status.Conditions locally (no API call) so callers
// can stage several condition updates and flush them with a single Status().Update.
func setCondition(proj *projectionv1.Projection, condType string, status metav1.ConditionStatus, reason, message string) {
	setConditionOn(&proj.Status.Conditions, condType, status, reason, message)
}

// setClusterCondition is the cluster-tier sibling of setCondition.
func setClusterCondition(cp *projectionv1.ClusterProjection, condType string, status metav1.ConditionStatus, reason, message string) {
	setConditionOn(&cp.Status.Conditions, condType, status, reason, message)
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
	reconcileTotal.WithLabelValues(kindProjection, resultSourceError).Inc()
	if err := r.Status().Update(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

// failDestination records a failure that happened during the write
// stage. By the time we get here we've already fetched the source, so
// SourceResolved is True; DestinationWritten flips to False. Ready
// mirrors the destination-side reason. Callers are responsible for
// emitting the event — failDestination only touches status, conditions,
// and metrics. Avoiding a second emit here keeps the client-go events
// broadcaster from aggregating two records into one that drops the
// action field.
func (r *ProjectionReconciler) failDestination(ctx context.Context, proj *projectionv1.Projection, resolvedVersion, reason, msg string) (ctrl.Result, error) {
	srMsg := resolvedVersionMessage(proj.Spec.Source, resolvedVersion)
	setCondition(proj, conditionSourceResolved, metav1.ConditionTrue, "Resolved", srMsg)
	setCondition(proj, conditionDestinationWritten, metav1.ConditionFalse, reason, msg)
	setCondition(proj, conditionReady, metav1.ConditionFalse, reason, msg)
	switch reason {
	case "DestinationConflict":
		reconcileTotal.WithLabelValues(kindProjection, resultConflict).Inc()
	default:
		reconcileTotal.WithLabelValues(kindProjection, resultDestinationError).Inc()
	}
	if err := r.Status().Update(ctx, proj); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.RequeueInterval}, nil
}

// markAllReady flips all three conditions to True and stamps the resolved
// destination name on status in a single update. The destination name
// surfaces on the printcolumn so kubectl get displays it without the
// caller having to chase the source ref.
func (r *ProjectionReconciler) markAllReady(ctx context.Context, proj *projectionv1.Projection, resolvedVersion string) error {
	msg := resolvedVersionMessage(proj.Spec.Source, resolvedVersion)
	setCondition(proj, conditionSourceResolved, metav1.ConditionTrue, "Resolved", msg)
	setCondition(proj, conditionDestinationWritten, metav1.ConditionTrue, "Projected", "")
	setCondition(proj, conditionReady, metav1.ConditionTrue, "Projected", "")
	_, destName := destinationCoords(proj)
	proj.Status.DestinationName = destName
	if err := r.Status().Update(ctx, proj); err != nil {
		return err
	}
	reconcileTotal.WithLabelValues(kindProjection, resultSuccess).Inc()
	return nil
}
