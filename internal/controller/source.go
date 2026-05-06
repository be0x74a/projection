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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// projectableAnnotation is the source-side opt-in/opt-out annotation key.
// See checkSourceProjectable for how its value is interpreted.
const projectableAnnotation = "projection.sh/projectable"

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

// sourceKey is the canonical string key identifying a source object across
// both the field indexer and the event-mapping function. The version is
// intentionally omitted: source events always carry a resolved GVK, but a
// Projection may reference its source via an unpinned form (e.g. apps/*).
// Joining on (group, kind, namespace, name) keeps both sides in agreement
// regardless of which served version the apiserver delivered the event for.
func sourceKey(group, kind, namespace, name string) string {
	return group + "/" + kind + "/" + namespace + "/" + name
}

// resolveGVR maps a SourceRef's apiVersion+kind to a concrete GVR via the
// cached RESTMapper. The second return value is the version the RESTMapper
// picked — equal to gv.Version when the user pinned a version, or the
// preferred served version when unpinned (gv.Version == "*"). Callers
// surface the resolved version in the SourceResolved condition message
// for operator-visibility.
func (d *ControllerDeps) resolveGVR(src projectionv1.SourceRef) (schema.GroupVersionResource, string, error) {
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
		mapping, err = d.RESTMapper.RESTMapping(gk)
	default:
		// Pinned to a specific version (today's behavior).
		mapping, err = d.RESTMapper.RESTMapping(gk, gv.Version)
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
