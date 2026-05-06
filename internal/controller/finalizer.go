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

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// finalizerName is the projection-owned finalizer stamped on every Projection
// so the controller can run cleanup before the apiserver garbage-collects it.
const finalizerName = "projection.sh/finalizer"

// handleDeletion runs the deletion path for a Projection whose
// DeletionTimestamp has been set. Returns handled=true when the caller should
// short-circuit Reconcile. A non-nil err is propagated so controller-runtime
// retries with backoff. When handled=false, the Projection is alive and the
// caller should continue with normal reconcile.
func (d *ControllerDeps) handleDeletion(ctx context.Context, proj *projectionv1.Projection) (handled bool, err error) {
	if proj.DeletionTimestamp.IsZero() {
		return false, nil
	}
	if controllerutil.ContainsFinalizer(proj, finalizerName) {
		if err := d.deleteDestination(ctx, proj); err != nil {
			log.FromContext(ctx).Error(err, "deleting destination")
			return true, err
		}
		controllerutil.RemoveFinalizer(proj, finalizerName)
		if err := d.Update(ctx, proj); err != nil {
			return true, err
		}
	}
	return true, nil
}

// ensureFinalizer adds the projection finalizer to proj if it's not already
// present. r.Update mutates proj in place with the apiserver-returned
// ResourceVersion, so a follow-up r.Status().Update later in Reconcile won't
// conflict — that's why we don't requeue here.
func (d *ControllerDeps) ensureFinalizer(ctx context.Context, proj *projectionv1.Projection) error {
	if controllerutil.ContainsFinalizer(proj, finalizerName) {
		return nil
	}
	controllerutil.AddFinalizer(proj, finalizerName)
	return d.Update(ctx, proj)
}
