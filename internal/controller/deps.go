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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// ControllerDeps bundles dependencies shared by every projection reconciler
// (namespaced and cluster-scoped). Each reconciler embeds one. Reconciler-
// specific state (the watched-sources map, the controller handle, requeue
// interval, source mode, …) lives on the reconciler itself, not here.
//
// Exported so cmd/main.go can construct it. Field names match the historical
// ProjectionReconciler shape so existing callers (cmd/main.go, tests) keep
// compiling with only the construction-shape change of wrapping the apiserver
// dependencies in `ControllerDeps: &ControllerDeps{...}`.
type ControllerDeps struct {
	client.Client
	Scheme        *runtime.Scheme
	DynamicClient dynamic.Interface
	RESTMapper    apimeta.RESTMapper
	Recorder      events.EventRecorder
}

// emit records a Kubernetes Event against the Projection. Nil-safe so unit
// tests that build a reconciler directly (without SetupWithManager) don't
// need to plumb a recorder. action is the UpperCamelCase verb describing
// what the controller did (e.g. Create, Update, Resolve); reason is the
// categorical outcome tag.
func (d *ControllerDeps) emit(proj *projectionv1.Projection, eventType, reason, action, message string) {
	if d == nil || d.Recorder == nil {
		return
	}
	d.Recorder.Eventf(proj, nil, eventType, reason, action, "%s", message)
}
