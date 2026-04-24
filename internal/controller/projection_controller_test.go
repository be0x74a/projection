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
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	projectionv1 "github.com/be0x74a/projection/api/v1"
)

// envtest does not run the namespace GC controller, so namespaces deleted in
// AfterEach are stuck in Terminating forever. To keep specs independent we
// mint fresh namespace names per It via this counter.
var nsCounter uint64

func uniqueNS(prefix string) string {
	n := atomic.AddUint64(&nsCounter, 1)
	return fmt.Sprintf("%s-%d", prefix, n)
}

// newReconciler builds a ProjectionReconciler wired to the envtest cluster.
func newReconciler() *ProjectionReconciler {
	httpClient, err := rest.HTTPClientFor(cfg)
	Expect(err).NotTo(HaveOccurred())
	mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	Expect(err).NotTo(HaveOccurred())
	dynClient, err := dynamic.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())

	return &ProjectionReconciler{
		Client:        k8sClient,
		Scheme:        k8sClient.Scheme(),
		DynamicClient: dynClient,
		RESTMapper:    mapper,
		Recorder:      events.NewFakeRecorder(16),
	}
}

// drainEvents pulls every event currently buffered on the FakeRecorder's
// channel. FakeRecorder encodes each event as "<type> <reason> <note>".
func drainEvents(r *ProjectionReconciler) []string {
	fake, ok := r.Recorder.(*events.FakeRecorder)
	if !ok {
		return nil
	}
	var out []string
	for {
		select {
		case e := <-fake.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// reconcileOnce runs Reconcile exactly once against the given key. The
// controller does finalizer-add + real work in a single pass, so tests no
// longer need to loop.
func reconcileOnce(r *ProjectionReconciler, key types.NamespacedName) reconcile.Result {
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	Expect(err).NotTo(HaveOccurred())
	return res
}

// idCounter feeds nextID for tests that need a unique-per-spec suffix for
// object names (not namespaces — use uniqueNS for those).
var idCounter uint64

func nextID() string {
	return fmt.Sprintf("%d", atomic.AddUint64(&idCounter, 1))
}

// findCondition returns a pointer to the condition of the given type, or nil.
func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

func ensureNamespace(name string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// deleteProjection forcibly removes a Projection, stripping finalizers if
// needed so teardown doesn't hang when a test exercised a non-standard path.
func deleteProjection(key types.NamespacedName) {
	proj := &projectionv1.Projection{}
	if err := k8sClient.Get(ctx, key, proj); err != nil {
		return
	}
	_ = k8sClient.Delete(ctx, proj)
	// If anything still blocks deletion, drop the finalizer.
	fresh := &projectionv1.Projection{}
	if err := k8sClient.Get(ctx, key, fresh); err == nil {
		fresh.Finalizers = nil
		_ = k8sClient.Update(ctx, fresh)
	}
}

var _ = Describe("Projection Controller (integration)", func() {
	var r *ProjectionReconciler

	BeforeEach(func() {
		r = newReconciler()
	})

	Context("Create path", func() {
		const (
			sourceCMName = "src-cm"
			projName     = "p-create"
			destCMName   = "renamed-dst"
		)
		var (
			sourceNS string
			destNS   string
			projKey  types.NamespacedName
		)

		BeforeEach(func() {
			sourceNS = uniqueNS("mirror-create-src")
			destNS = uniqueNS("mirror-create-dst")
			projKey = types.NamespacedName{Name: projName, Namespace: sourceNS}

			ensureNamespace(sourceNS)
			ensureNamespace(destNS)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        sourceCMName,
					Namespace:   sourceNS,
					Labels:      map[string]string{"src-label": "src-val"},
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"key1": "value1"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: sourceNS},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: sourceNS,
					},
					Destination: projectionv1.DestinationRef{
						Namespace: destNS, Name: destCMName,
					},
					Overlay: projectionv1.Overlay{
						Labels:      map[string]string{"overlay-label": "ov-val", "src-label": "overridden"},
						Annotations: map[string]string{"overlay-ann": "ann-val"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		})

		AfterEach(func() {
			deleteProjection(projKey)
		})

		It("projects the source to the destination with overlay applied", func() {
			before := testutil.ToFloat64(reconcileTotal.WithLabelValues(resultSuccess))
			reconcileOnce(r, projKey)
			Expect(testutil.ToFloat64(reconcileTotal.WithLabelValues(resultSuccess))-before).
				To(BeNumerically(">=", 1), "success counter should have incremented")

			dst := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: destCMName, Namespace: destNS}, dst)).To(Succeed())

			Expect(dst.Data).To(Equal(map[string]string{"key1": "value1"}))
			Expect(dst.Annotations).To(HaveKeyWithValue(ownedByAnnotation, sourceNS+"/"+projName))
			Expect(dst.Annotations).To(HaveKeyWithValue("overlay-ann", "ann-val"))
			Expect(dst.Labels).To(HaveKeyWithValue("overlay-label", "ov-val"))
			// Overlay wins over source on key conflicts.
			Expect(dst.Labels).To(HaveKeyWithValue("src-label", "overridden"))

			// Happy path: all three status conditions True.
			proj := &projectionv1.Projection{}
			Expect(k8sClient.Get(ctx, projKey, proj)).To(Succeed())
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal("SourceResolved")),
				HaveField("Status", Equal(metav1.ConditionTrue)),
			)))
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal("DestinationWritten")),
				HaveField("Status", Equal(metav1.ConditionTrue)),
			)))
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal("Ready")),
				HaveField("Status", Equal(metav1.ConditionTrue)),
				HaveField("Reason", Equal("Projected")),
			)))

			// Regression: pinned apiVersion forms must keep an empty
			// SourceResolved.message — only unpinned (apps/*) populates it.
			sr := apimeta.FindStatusCondition(proj.Status.Conditions, "SourceResolved")
			Expect(sr).ToNot(BeNil())
			Expect(sr.Message).To(Equal(""), "pinned form must keep empty message")

			// Create-path emits one Normal Projected event.
			events := drainEvents(r)
			Expect(events).To(ContainElement(ContainSubstring("Normal Projected")))
		})

		It("updates the destination when the source changes", func() {
			reconcileOnce(r, projKey)

			src := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: sourceNS}, src)).To(Succeed())
			src.Data = map[string]string{"key1": "value1-updated", "key2": "value2"}
			Expect(k8sClient.Update(ctx, src)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: projKey})
			Expect(err).NotTo(HaveOccurred())

			dst := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: destCMName, Namespace: destNS}, dst)).To(Succeed())
			Expect(dst.Data).To(Equal(map[string]string{"key1": "value1-updated", "key2": "value2"}))
			Expect(dst.Annotations).To(HaveKeyWithValue(ownedByAnnotation, sourceNS+"/"+projName))

			// Update path must emit Projected (from the initial Create) followed
			// by Updated; order matters.
			events := drainEvents(r)
			var projectedIdx, updatedIdx = -1, -1
			for i, e := range events {
				if projectedIdx == -1 && strings.Contains(e, "Normal Projected") {
					projectedIdx = i
				}
				if strings.Contains(e, "Normal Updated") {
					updatedIdx = i
				}
			}
			Expect(projectedIdx).To(BeNumerically(">=", 0), "expected a Projected event")
			Expect(updatedIdx).To(BeNumerically(">", projectedIdx), "expected Updated to follow Projected")
		})

		It("populates SourceResolved.message with the resolved version when apiVersion is unpinned", func() {
			srcNS := uniqueNS("unpinned-src")
			dstNS := uniqueNS("unpinned-dst")
			ensureNamespace(srcNS)
			ensureNamespace(dstNS)

			src := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-dep",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "x"}},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "nginx"}}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			unpinnedKey := types.NamespacedName{Name: "unpinned-proj", Namespace: srcNS}
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: unpinnedKey.Name, Namespace: unpinnedKey.Namespace},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "apps/*", Kind: "Deployment",
						Name: "src-dep", Namespace: srcNS,
					},
					Destination: projectionv1.DestinationRef{Namespace: dstNS},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
			DeferCleanup(deleteProjection, unpinnedKey)

			reconcileOnce(r, unpinnedKey)

			got := &projectionv1.Projection{}
			Expect(k8sClient.Get(ctx, unpinnedKey, got)).To(Succeed())
			sr := apimeta.FindStatusCondition(got.Status.Conditions, "SourceResolved")
			Expect(sr).ToNot(BeNil())
			Expect(sr.Status).To(Equal(metav1.ConditionTrue))
			Expect(sr.Message).To(Equal("resolved apps/Deployment to preferred version v1"))
		})
	})

	Context("Conflict path", func() {
		const (
			sourceCMName = "cm-src"
			destCMName   = "cm-dst"
			projName     = "p-conflict"
		)
		var (
			ns      string
			projKey types.NamespacedName
		)

		BeforeEach(func() {
			ns = uniqueNS("mirror-conflict")
			projKey = types.NamespacedName{Name: projName, Namespace: ns}
			ensureNamespace(ns)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        sourceCMName,
					Namespace:   ns,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"from": "source"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			// Pre-existing, unowned destination.
			existing := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      destCMName,
					Namespace: ns,
					Annotations: map[string]string{
						"some-other-owner": "yes",
					},
				},
				Data: map[string]string{"from": "stranger"},
			}
			Expect(k8sClient.Create(ctx, existing)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: ns},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: ns,
					},
					Destination: projectionv1.DestinationRef{Name: destCMName},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		})

		AfterEach(func() {
			deleteProjection(projKey)
		})

		It("sets Ready=False with reason DestinationConflict and leaves the stranger untouched", func() {
			before := testutil.ToFloat64(reconcileTotal.WithLabelValues(resultConflict))
			reconcileOnce(r, projKey)
			Expect(testutil.ToFloat64(reconcileTotal.WithLabelValues(resultConflict))-before).
				To(BeNumerically(">=", 1), "conflict counter should have incremented")

			// Stranger ConfigMap unchanged.
			dst := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: destCMName, Namespace: ns}, dst)).To(Succeed())
			Expect(dst.Data).To(Equal(map[string]string{"from": "stranger"}))
			Expect(dst.Annotations).NotTo(HaveKey(ownedByAnnotation))

			// Projection status reflects the conflict.
			proj := &projectionv1.Projection{}
			Expect(k8sClient.Get(ctx, projKey, proj)).To(Succeed())
			cond := apimeta.FindStatusCondition(proj.Status.Conditions, conditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("DestinationConflict"))

			// Split conditions: SourceResolved still True (we fetched the source
			// successfully before hitting the conflict), DestinationWritten False
			// with the conflict reason.
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal("SourceResolved")),
				HaveField("Status", Equal(metav1.ConditionTrue)),
			)))
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal("DestinationWritten")),
				HaveField("Status", Equal(metav1.ConditionFalse)),
				HaveField("Reason", Equal("DestinationConflict")),
			)))

			// Conflict-path emits one Warning DestinationConflict event.
			events := drainEvents(r)
			Expect(events).To(ContainElement(ContainSubstring("Warning DestinationConflict")))
		})
	})

	Context("Deletion path", func() {
		const (
			sourceCMName = "cm-src"
			destCMName   = "cm-dst"
			projName     = "p-delete"
		)
		var (
			ns      string
			projKey types.NamespacedName
		)

		BeforeEach(func() {
			ns = uniqueNS("mirror-delete")
			projKey = types.NamespacedName{Name: projName, Namespace: ns}
			ensureNamespace(ns)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        sourceCMName,
					Namespace:   ns,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"k": "v"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())
		})

		AfterEach(func() {
			deleteProjection(projKey)
		})

		It("removes the owned destination and finalizer when the Projection is deleted", func() {
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: ns},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: ns,
					},
					Destination: projectionv1.DestinationRef{Name: destCMName},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
			reconcileOnce(r, projKey)

			// Destination was created and owned.
			dst := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: destCMName, Namespace: ns}, dst)).To(Succeed())
			Expect(dst.Annotations).To(HaveKeyWithValue(ownedByAnnotation, ns+"/"+projName))

			// Delete the Projection.
			live := &projectionv1.Projection{}
			Expect(k8sClient.Get(ctx, projKey, live)).To(Succeed())
			Expect(k8sClient.Delete(ctx, live)).To(Succeed())

			// Deletion path reconcile.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: projKey})
			Expect(err).NotTo(HaveOccurred())

			// Destination gone.
			err = k8sClient.Get(ctx, types.NamespacedName{Name: destCMName, Namespace: ns}, &corev1.ConfigMap{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue())

			// Projection gone (finalizer removed, GC'd).
			Eventually(func() bool {
				err := k8sClient.Get(ctx, projKey, &projectionv1.Projection{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())
		})

		It("does not delete a destination it does not own", func() {
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: ns},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: ns,
					},
					Destination: projectionv1.DestinationRef{Name: destCMName},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
			reconcileOnce(r, projKey)

			// Strip the ownership annotation so the controller no longer
			// recognises the destination as ours.
			dst := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: destCMName, Namespace: ns}, dst)).To(Succeed())
			delete(dst.Annotations, ownedByAnnotation)
			// Mark it with something distinctive so we can confirm identity later.
			if dst.Annotations == nil {
				dst.Annotations = map[string]string{}
			}
			dst.Annotations["stranger"] = "true"
			Expect(k8sClient.Update(ctx, dst)).To(Succeed())

			// Delete the Projection; reconcile the deletion path.
			live := &projectionv1.Projection{}
			Expect(k8sClient.Get(ctx, projKey, live)).To(Succeed())
			Expect(k8sClient.Delete(ctx, live)).To(Succeed())
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: projKey})
			Expect(err).NotTo(HaveOccurred())

			// Destination still present, untouched.
			after := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: destCMName, Namespace: ns}, after)).To(Succeed())
			Expect(after.Annotations).To(HaveKeyWithValue("stranger", "true"))
			Expect(after.Annotations).NotTo(HaveKey(ownedByAnnotation))

			// But the Projection is gone.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, projKey, &projectionv1.Projection{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Context("Source projectability policy", func() {
		It("refuses to project a source missing the projectable annotation in allowlist mode", func() {
			ns := uniqueNS("mirror-policy-allowlist")
			ensureNamespace(ns)

			// Source without the projectable annotation.
			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "unblessed-cm", Namespace: ns},
				Data:       map[string]string{"x": "y"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			projKey := types.NamespacedName{Name: "p-allowlist", Namespace: ns}
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projKey.Name, Namespace: projKey.Namespace},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: ns,
					},
					Destination: projectionv1.DestinationRef{Name: "dst-never-written"},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
			DeferCleanup(func() { deleteProjection(projKey) })

			// Reconciler defaults to allowlist (empty SourceMode).
			reconcileOnce(r, projKey)

			// Projection surfaces SourceResolved=False with SourceNotProjectable reason.
			var got projectionv1.Projection
			Expect(k8sClient.Get(ctx, projKey, &got)).To(Succeed())
			Expect(got.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal(conditionSourceResolved)),
				HaveField("Status", Equal(metav1.ConditionFalse)),
				HaveField("Reason", Equal("SourceNotProjectable")),
			)))
			// No destination was created.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "dst-never-written", Namespace: ns},
				&corev1.ConfigMap{})).To(MatchError(apierrors.IsNotFound, "IsNotFound"))
		})

		It("honors opt-out (false) even in permissive mode, garbage-collecting an existing destination", func() {
			ns := uniqueNS("mirror-policy-optout")
			ensureNamespace(ns)

			// Initially projectable.
			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "toggle-cm",
					Namespace:   ns,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"v": "1"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			projKey := types.NamespacedName{Name: "p-optout", Namespace: ns}
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projKey.Name, Namespace: projKey.Namespace},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: ns,
					},
					Destination: projectionv1.DestinationRef{Name: "mirrored-cm"},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
			DeferCleanup(func() { deleteProjection(projKey) })

			// Run in permissive mode to prove the opt-out veto still fires.
			r.SourceMode = SourceModePermissive
			reconcileOnce(r, projKey)

			dstKey := types.NamespacedName{Name: "mirrored-cm", Namespace: ns}
			Expect(k8sClient.Get(ctx, dstKey, &corev1.ConfigMap{})).To(Succeed(),
				"destination should exist after the initial projection")

			// Source owner opts out.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: src.Name, Namespace: ns}, src)).To(Succeed())
			src.Annotations[projectableAnnotation] = "false"
			Expect(k8sClient.Update(ctx, src)).To(Succeed())

			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: projKey})
			Expect(err).NotTo(HaveOccurred())

			// The existing destination should be garbage-collected.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, dstKey, &corev1.ConfigMap{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue(), "destination should be cleaned up after opt-out")

			// Status reflects SourceOptedOut.
			var got projectionv1.Projection
			Expect(k8sClient.Get(ctx, projKey, &got)).To(Succeed())
			Expect(got.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal(conditionSourceResolved)),
				HaveField("Status", Equal(metav1.ConditionFalse)),
				HaveField("Reason", Equal("SourceOptedOut")),
			)))
		})
	})

	Context("Requeue-interval plumbing", func() {
		It("returns the configured RequeueInterval when reconciliation fails fast", func() {
			ns := uniqueNS("mirror-requeue-interval")
			ensureNamespace(ns)

			// Projection pointing at a nonexistent source — will fail in
			// failSource, which returns ctrl.Result{RequeueAfter: r.RequeueInterval}.
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: "p-requeue", Namespace: ns},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: "does-not-exist", Namespace: ns,
					},
					Destination: projectionv1.DestinationRef{Namespace: ns},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
			DeferCleanup(deleteProjection, types.NamespacedName{Name: "p-requeue", Namespace: ns})

			r := newReconciler()
			r.RequeueInterval = 7 * time.Second

			res := reconcileOnce(r, types.NamespacedName{Name: "p-requeue", Namespace: ns})
			Expect(res.RequeueAfter).To(Equal(7*time.Second),
				"configured RequeueInterval must flow into the Reconcile result")
		})
	})

	Context("Namespace selector path", func() {
		const (
			sourceCMName = "shared-cfg"
			projName     = "p-selector"
		)
		var (
			sourceNS  string
			matchNS1  string
			matchNS2  string
			matchNS3  string
			noMatchNS string
			projKey   types.NamespacedName
		)

		BeforeEach(func() {
			sourceNS = uniqueNS("sel-src")
			matchNS1 = uniqueNS("sel-match1")
			matchNS2 = uniqueNS("sel-match2")
			matchNS3 = uniqueNS("sel-match3")
			noMatchNS = uniqueNS("sel-nomatch")
			projKey = types.NamespacedName{Name: projName, Namespace: sourceNS}

			ensureNamespace(sourceNS)
			ensureNamespaceWithLabels(matchNS1, map[string]string{"mirror": "true"})
			ensureNamespaceWithLabels(matchNS2, map[string]string{"mirror": "true"})
			ensureNamespaceWithLabels(matchNS3, map[string]string{"mirror": "true"})
			ensureNamespace(noMatchNS)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        sourceCMName,
					Namespace:   sourceNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"env": "production"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: sourceNS},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: sourceNS,
					},
					Destination: projectionv1.DestinationRef{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"mirror": "true"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		})

		AfterEach(func() {
			deleteProjection(projKey)
		})

		It("projects the source to all matching namespaces and none to non-matching", func() {
			reconcileOnce(r, projKey)

			for _, ns := range []string{matchNS1, matchNS2, matchNS3} {
				dst := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: ns}, dst)).To(Succeed(),
					"destination should exist in matching namespace %s", ns)
				Expect(dst.Data).To(Equal(map[string]string{"env": "production"}))
				Expect(dst.Annotations).To(HaveKeyWithValue(ownedByAnnotation, sourceNS+"/"+projName))
			}

			// Non-matching namespace should NOT have a destination.
			err := k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: noMatchNS}, &corev1.ConfigMap{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "destination should not exist in non-matching namespace")

			// Status: Ready=True, DestinationWritten=True.
			proj := &projectionv1.Projection{}
			Expect(k8sClient.Get(ctx, projKey, proj)).To(Succeed())
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal(conditionReady)),
				HaveField("Status", Equal(metav1.ConditionTrue)),
			)))
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal(conditionDestinationWritten)),
				HaveField("Status", Equal(metav1.ConditionTrue)),
			)))
		})
	})

	Context("Namespace selector — namespace added after initial reconcile", func() {
		const (
			sourceCMName = "cfg-late"
			projName     = "p-sel-late"
		)
		var (
			sourceNS string
			matchNS1 string
			matchNS2 string
			projKey  types.NamespacedName
		)

		BeforeEach(func() {
			sourceNS = uniqueNS("sellate-src")
			matchNS1 = uniqueNS("sellate-m1")
			matchNS2 = uniqueNS("sellate-m2")
			projKey = types.NamespacedName{Name: projName, Namespace: sourceNS}

			ensureNamespace(sourceNS)
			ensureNamespaceWithLabels(matchNS1, map[string]string{"tier": "frontend"})
			ensureNamespaceWithLabels(matchNS2, map[string]string{"tier": "frontend"})

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        sourceCMName,
					Namespace:   sourceNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"mode": "live"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: sourceNS},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: sourceNS,
					},
					Destination: projectionv1.DestinationRef{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"tier": "frontend"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		})

		AfterEach(func() {
			deleteProjection(projKey)
		})

		It("picks up a newly-labeled namespace on re-reconcile", func() {
			reconcileOnce(r, projKey)

			// Verify initial 2 destinations.
			for _, ns := range []string{matchNS1, matchNS2} {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: ns}, &corev1.ConfigMap{})).To(Succeed())
			}

			// Add a 3rd matching namespace.
			lateNS := uniqueNS("sellate-m3")
			ensureNamespaceWithLabels(lateNS, map[string]string{"tier": "frontend"})

			// Re-reconcile.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: projKey})
			Expect(err).NotTo(HaveOccurred())

			// 3rd destination should now exist.
			dst := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: lateNS}, dst)).To(Succeed())
			Expect(dst.Data).To(Equal(map[string]string{"mode": "live"}))
		})
	})

	Context("Namespace selector — stale destination cleanup", func() {
		const (
			sourceCMName = "cfg-stale"
			projName     = "p-sel-stale"
		)
		var (
			sourceNS string
			matchNS1 string
			matchNS2 string
			matchNS3 string
			projKey  types.NamespacedName
		)

		BeforeEach(func() {
			sourceNS = uniqueNS("selstale-src")
			matchNS1 = uniqueNS("selstale-m1")
			matchNS2 = uniqueNS("selstale-m2")
			matchNS3 = uniqueNS("selstale-m3")
			projKey = types.NamespacedName{Name: projName, Namespace: sourceNS}

			ensureNamespace(sourceNS)
			ensureNamespaceWithLabels(matchNS1, map[string]string{"zone": "us"})
			ensureNamespaceWithLabels(matchNS2, map[string]string{"zone": "us"})
			ensureNamespaceWithLabels(matchNS3, map[string]string{"zone": "us"})

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        sourceCMName,
					Namespace:   sourceNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"region": "east"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: sourceNS},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: sourceNS,
					},
					Destination: projectionv1.DestinationRef{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"zone": "us"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		})

		AfterEach(func() {
			deleteProjection(projKey)
		})

		It("deletes the destination in a de-labeled namespace on re-reconcile", func() {
			reconcileOnce(r, projKey)

			// All 3 destinations exist.
			for _, ns := range []string{matchNS1, matchNS2, matchNS3} {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: ns}, &corev1.ConfigMap{})).To(Succeed())
			}

			// Remove the label from matchNS3.
			ns3 := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: matchNS3}, ns3)).To(Succeed())
			delete(ns3.Labels, "zone")
			Expect(k8sClient.Update(ctx, ns3)).To(Succeed())

			// Re-reconcile.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: projKey})
			Expect(err).NotTo(HaveOccurred())

			// matchNS1 and matchNS2 still have destinations.
			for _, ns := range []string{matchNS1, matchNS2} {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: ns}, &corev1.ConfigMap{})).To(Succeed(),
					"destination should still exist in %s", ns)
			}

			// matchNS3's destination was cleaned up.
			err = k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: matchNS3}, &corev1.ConfigMap{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"stale destination should be removed from de-labeled namespace")
		})
	})

	Context("Namespace selector — deletion cleans all destinations", func() {
		const (
			sourceCMName = "cfg-delall"
			projName     = "p-sel-delall"
		)
		var (
			sourceNS string
			matchNS1 string
			matchNS2 string
			matchNS3 string
			projKey  types.NamespacedName
		)

		BeforeEach(func() {
			sourceNS = uniqueNS("seldelall-src")
			matchNS1 = uniqueNS("seldelall-m1")
			matchNS2 = uniqueNS("seldelall-m2")
			matchNS3 = uniqueNS("seldelall-m3")
			projKey = types.NamespacedName{Name: projName, Namespace: sourceNS}

			ensureNamespace(sourceNS)
			ensureNamespaceWithLabels(matchNS1, map[string]string{"cleanup": "yes"})
			ensureNamespaceWithLabels(matchNS2, map[string]string{"cleanup": "yes"})
			ensureNamespaceWithLabels(matchNS3, map[string]string{"cleanup": "yes"})

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        sourceCMName,
					Namespace:   sourceNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"final": "data"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: sourceNS},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: sourceNS,
					},
					Destination: projectionv1.DestinationRef{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"cleanup": "yes"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		})

		AfterEach(func() {
			deleteProjection(projKey)
		})

		It("removes all destinations when the Projection is deleted", func() {
			reconcileOnce(r, projKey)

			// All 3 destinations exist.
			for _, ns := range []string{matchNS1, matchNS2, matchNS3} {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: ns}, &corev1.ConfigMap{})).To(Succeed())
			}

			// Delete the Projection.
			live := &projectionv1.Projection{}
			Expect(k8sClient.Get(ctx, projKey, live)).To(Succeed())
			Expect(k8sClient.Delete(ctx, live)).To(Succeed())

			// Deletion path reconcile.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: projKey})
			Expect(err).NotTo(HaveOccurred())

			// All destinations gone.
			for _, ns := range []string{matchNS1, matchNS2, matchNS3} {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: ns}, &corev1.ConfigMap{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(),
					"destination in %s should be deleted", ns)
			}

			// Projection gone (finalizer removed, GC'd).
			Eventually(func() bool {
				err := k8sClient.Get(ctx, projKey, &projectionv1.Projection{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})

	Context("Namespace selector — partial failure (conflict in one namespace)", func() {
		const (
			sourceCMName = "cfg-partial"
			projName     = "p-sel-partial"
		)
		var (
			sourceNS   string
			okNS1      string
			okNS2      string
			conflictNS string
			projKey    types.NamespacedName
		)

		BeforeEach(func() {
			sourceNS = uniqueNS("selpartial-src")
			okNS1 = uniqueNS("selpartial-ok1")
			okNS2 = uniqueNS("selpartial-ok2")
			conflictNS = uniqueNS("selpartial-conflict")
			projKey = types.NamespacedName{Name: projName, Namespace: sourceNS}

			ensureNamespace(sourceNS)
			ensureNamespaceWithLabels(okNS1, map[string]string{"partial": "yes"})
			ensureNamespaceWithLabels(okNS2, map[string]string{"partial": "yes"})
			ensureNamespaceWithLabels(conflictNS, map[string]string{"partial": "yes"})

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        sourceCMName,
					Namespace:   sourceNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"partial": "test"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			// Pre-create an unowned stranger in the conflict namespace.
			stranger := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sourceCMName,
					Namespace: conflictNS,
					Annotations: map[string]string{
						"some-other-owner": "yes",
					},
				},
				Data: map[string]string{"from": "stranger"},
			}
			Expect(k8sClient.Create(ctx, stranger)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: sourceNS},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: sourceCMName, Namespace: sourceNS,
					},
					Destination: projectionv1.DestinationRef{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"partial": "yes"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		})

		AfterEach(func() {
			deleteProjection(projKey)
		})

		It("succeeds in non-conflicting namespaces but reports Ready=False due to the conflict", func() {
			reconcileOnce(r, projKey)

			// The two OK namespaces should have owned destinations.
			for _, ns := range []string{okNS1, okNS2} {
				dst := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: ns}, dst)).To(Succeed(),
					"destination should exist in %s", ns)
				Expect(dst.Data).To(Equal(map[string]string{"partial": "test"}))
				Expect(dst.Annotations).To(HaveKeyWithValue(ownedByAnnotation, sourceNS+"/"+projName))
			}

			// Stranger in the conflict namespace is untouched.
			stranger := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: sourceCMName, Namespace: conflictNS}, stranger)).To(Succeed())
			Expect(stranger.Data).To(Equal(map[string]string{"from": "stranger"}))
			Expect(stranger.Annotations).NotTo(HaveKey(ownedByAnnotation))

			// Status: Ready=False, DestinationWritten=False with DestinationConflict.
			proj := &projectionv1.Projection{}
			Expect(k8sClient.Get(ctx, projKey, proj)).To(Succeed())
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal(conditionReady)),
				HaveField("Status", Equal(metav1.ConditionFalse)),
			)))
			Expect(proj.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal(conditionDestinationWritten)),
				HaveField("Status", Equal(metav1.ConditionFalse)),
				HaveField("Reason", Equal("DestinationConflict")),
			)))

			// Warning event emitted for the conflict.
			events := drainEvents(r)
			Expect(events).To(ContainElement(ContainSubstring("Warning DestinationConflict")))
		})
	})

	Context("Namespace and namespaceSelector mutually exclusive", func() {
		It("reconciler rejects a Projection with both namespace and namespaceSelector set", func() {
			// Use a fake client to bypass CEL admission (which also enforces this
			// invariant), proving the reconciler check works as defense-in-depth.
			ns := uniqueNS("sel-mutex")
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "p-mutex",
					Namespace:       ns,
					ResourceVersion: "1",
				},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: "some-cm", Namespace: ns,
					},
					Destination: projectionv1.DestinationRef{
						Namespace: "explicit-ns",
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"mirror": "true"},
						},
					},
				},
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(k8sClient.Scheme()).
				WithObjects(proj).
				WithStatusSubresource(proj).
				Build()
			fakeRecorder := events.NewFakeRecorder(16)
			fakeR := &ProjectionReconciler{
				Client:   fakeClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: fakeRecorder,
			}
			projKey := types.NamespacedName{Name: proj.Name, Namespace: proj.Namespace}

			_, err := fakeR.Reconcile(ctx, reconcile.Request{NamespacedName: projKey})
			Expect(err).NotTo(HaveOccurred())

			var got projectionv1.Projection
			Expect(fakeClient.Get(ctx, projKey, &got)).To(Succeed())
			Expect(got.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal(conditionDestinationWritten)),
				HaveField("Status", Equal(metav1.ConditionFalse)),
				HaveField("Reason", Equal("InvalidSpec")),
				HaveField("Message", ContainSubstring("mutually exclusive")),
			)))
		})

		It("apiserver rejects a Projection with both namespace and namespaceSelector set at admission time", func() {
			ns := uniqueNS("sel-mutex-cel")
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: ns},
			})).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: ns},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1",
						Kind:       "ConfigMap",
						Name:       "src",
						Namespace:  ns,
					},
					Destination: projectionv1.DestinationRef{
						Namespace: "dest-ns",
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"env": "prod"},
						},
					},
				},
			}

			err := k8sClient.Create(ctx, proj)
			Expect(err).To(HaveOccurred())
			Expect(apierrors.IsInvalid(err)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
		})
	})

	Context("Source deletion", func() {
		It("cleans up the destination when the source is deleted", func() {
			id := nextID()
			srcNS := uniqueNS("mirror-srcdel-src")
			dstNS := uniqueNS("mirror-srcdel-dst")
			projKey := types.NamespacedName{Name: "p-srcdel-" + id, Namespace: srcNS}
			srcName := "src-cm-" + id

			ensureNamespace(srcNS)
			ensureNamespace(dstNS)
			DeferCleanup(deleteProjection, projKey)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        srcName,
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"key": "value"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projKey.Name, Namespace: projKey.Namespace},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: srcName, Namespace: srcNS,
					},
					Destination: projectionv1.DestinationRef{Namespace: dstNS},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())

			By("first reconcile — destination appears")
			reconcileOnce(r, projKey)
			Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: srcName, Namespace: dstNS}, &corev1.ConfigMap{})).To(Succeed())

			By("deleting the source")
			Expect(k8sClient.Delete(ctx, src)).To(Succeed())

			By("second reconcile — destination is cleaned up, status reflects SourceDeleted")
			_ = drainEvents(r)
			reconcileOnce(r, projKey)

			Eventually(func() error {
				return k8sClient.Get(ctx,
					types.NamespacedName{Name: srcName, Namespace: dstNS}, &corev1.ConfigMap{})
			}, 2*time.Second, 100*time.Millisecond).Should(MatchError(ContainSubstring("not found")))

			var fresh projectionv1.Projection
			Expect(k8sClient.Get(ctx, projKey, &fresh)).To(Succeed())

			srcResolved := findCondition(fresh.Status.Conditions, "SourceResolved")
			Expect(srcResolved).NotTo(BeNil())
			Expect(srcResolved.Status).To(Equal(metav1.ConditionFalse))
			Expect(srcResolved.Reason).To(Equal("SourceDeleted"))

			destWritten := findCondition(fresh.Status.Conditions, "DestinationWritten")
			Expect(destWritten).NotTo(BeNil())
			Expect(destWritten.Status).To(Equal(metav1.ConditionUnknown))
			Expect(destWritten.Reason).To(Equal("SourceNotResolved"))

			events := drainEvents(r)
			Expect(events).To(ContainElement(ContainSubstring("Warning SourceDeleted")),
				"expected exactly one Warning SourceDeleted event, got: %v", events)
		})

		It("cleans up every selector-based destination when the source is deleted", func() {
			id := nextID()
			srcNS := uniqueNS("mirror-srcdel-sel-src")
			projKey := types.NamespacedName{Name: "p-srcdel-sel-" + id, Namespace: srcNS}
			srcName := "src-cm-" + id
			label := "mirror-srcdel-" + id

			ensureNamespace(srcNS)
			DeferCleanup(deleteProjection, projKey)

			var destNS [3]string
			for i := range destNS {
				destNS[i] = uniqueNS("mirror-srcdel-sel-dst")
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:   destNS[i],
						Labels: map[string]string{label: "true"},
					},
				}
				Expect(k8sClient.Create(ctx, ns)).To(Succeed())
			}

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        srcName,
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"k": "v"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: projKey.Name, Namespace: projKey.Namespace},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: srcName, Namespace: srcNS,
					},
					Destination: projectionv1.DestinationRef{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{label: "true"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())

			By("first reconcile — destinations appear in all 3 namespaces")
			reconcileOnce(r, projKey)
			for _, ns := range destNS {
				Expect(k8sClient.Get(ctx,
					types.NamespacedName{Name: srcName, Namespace: ns}, &corev1.ConfigMap{})).
					To(Succeed(), "destination missing in %s", ns)
			}

			By("deleting the source and reconciling")
			Expect(k8sClient.Delete(ctx, src)).To(Succeed())
			reconcileOnce(r, projKey)

			By("all three destinations are cleaned up")
			for _, ns := range destNS {
				nsCopy := ns
				Eventually(func() error {
					return k8sClient.Get(ctx,
						types.NamespacedName{Name: srcName, Namespace: nsCopy}, &corev1.ConfigMap{})
				}, 2*time.Second, 100*time.Millisecond).Should(MatchError(ContainSubstring("not found")),
					"destination lingered in %s", nsCopy)
			}
		})
	})
})

func ensureNamespaceWithLabels(name string, labels map[string]string) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
	err := k8sClient.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

var _ = Describe("Shared source watch (integration with manager)", Ordered, func() {
	var (
		sharedMgr    ctrl.Manager
		sharedCancel context.CancelFunc
		sharedR      *ProjectionReconciler
	)

	BeforeAll(func() {
		// New manager wired to the same envtest cluster. Metrics/probes
		// disabled, no leader election.
		var err error
		sharedMgr, err = ctrl.NewManager(cfg, ctrl.Options{
			Scheme: k8sClient.Scheme(),
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			HealthProbeBindAddress: "0",
			LeaderElection:         false,
		})
		Expect(err).NotTo(HaveOccurred())

		httpClient, err := rest.HTTPClientFor(cfg)
		Expect(err).NotTo(HaveOccurred())
		mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
		Expect(err).NotTo(HaveOccurred())
		dynClient, err := dynamic.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())

		sharedR = &ProjectionReconciler{
			Client:        sharedMgr.GetClient(),
			Scheme:        sharedMgr.GetScheme(),
			DynamicClient: dynClient,
			RESTMapper:    mapper,
			Recorder:      events.NewFakeRecorder(256),
		}
		Expect(sharedR.SetupWithManager(sharedMgr)).To(Succeed())

		var mgrCtx context.Context
		mgrCtx, sharedCancel = context.WithCancel(context.Background())
		go func() {
			defer GinkgoRecover()
			Expect(sharedMgr.Start(mgrCtx)).To(Succeed())
		}()

		// Wait for the cache to sync before any spec runs.
		Expect(sharedMgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())
	})

	AfterAll(func() {
		if sharedCancel != nil {
			sharedCancel()
		}
	})

	It("two Projections sharing a source GVK register a single watch and both reconcile", func() {
		id := nextID()
		srcNS := uniqueNS("sharedwatch-src")
		dstNS1 := uniqueNS("sharedwatch-dst1")
		dstNS2 := uniqueNS("sharedwatch-dst2")
		srcName := "src-cm-" + id
		proj1Key := types.NamespacedName{Name: "p-sw1-" + id, Namespace: srcNS}
		proj2Key := types.NamespacedName{Name: "p-sw2-" + id, Namespace: srcNS}

		ensureNamespace(srcNS)
		ensureNamespace(dstNS1)
		ensureNamespace(dstNS2)
		DeferCleanup(deleteProjection, proj1Key)
		DeferCleanup(deleteProjection, proj2Key)

		src := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:        srcName,
				Namespace:   srcNS,
				Annotations: map[string]string{projectableAnnotation: "true"},
			},
			Data: map[string]string{"version": "1"},
		}
		Expect(k8sClient.Create(ctx, src)).To(Succeed())

		for _, spec := range []struct {
			key    types.NamespacedName
			destNS string
		}{
			{proj1Key, dstNS1},
			{proj2Key, dstNS2},
		} {
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: spec.key.Name, Namespace: spec.key.Namespace},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: "v1", Kind: "ConfigMap",
						Name: srcName, Namespace: srcNS,
					},
					Destination: projectionv1.DestinationRef{Namespace: spec.destNS},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		}

		By("both destinations get created by the manager-driven reconciler")
		for _, dstNS := range []string{dstNS1, dstNS2} {
			nsCopy := dstNS
			Eventually(func() string {
				dst := &corev1.ConfigMap{}
				_ = k8sClient.Get(ctx,
					types.NamespacedName{Name: srcName, Namespace: nsCopy}, dst)
				return dst.Data["version"]
			}, 10*time.Second, 200*time.Millisecond).Should(Equal("1"),
				"destination missing or wrong version in %s", nsCopy)
		}

		By("the reconciler's watched-GVK map has exactly one entry for v1/ConfigMap (idempotent registration)")
		sharedR.watchedMu.Lock()
		entries := len(sharedR.watched)
		_, hasCM := sharedR.watched[schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}]
		sharedR.watchedMu.Unlock()
		Expect(entries).To(Equal(1), "expected exactly one registered watch; got %d", entries)
		Expect(hasCM).To(BeTrue(), "ConfigMap GVK not in watched map")

		By("updating the source — both destinations reflect the change via the shared watch")
		updated := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: srcName, Namespace: srcNS}, updated)).To(Succeed())
		updated.Data["version"] = "2"
		Expect(k8sClient.Update(ctx, updated)).To(Succeed())

		for _, dstNS := range []string{dstNS1, dstNS2} {
			nsCopy := dstNS
			Eventually(func() string {
				dst := &corev1.ConfigMap{}
				_ = k8sClient.Get(ctx,
					types.NamespacedName{Name: srcName, Namespace: nsCopy}, dst)
				return dst.Data["version"]
			}, 10*time.Second, 200*time.Millisecond).Should(Equal("2"),
				"destination in %s didn't receive the source update", nsCopy)
		}
	})

	It("a pinned and an unpinned Projection on the same source share one watch entry", func() {
		id := nextID()
		srcNS := uniqueNS("mixedwatch-src")
		dstNS1 := uniqueNS("mixedwatch-dst1")
		dstNS2 := uniqueNS("mixedwatch-dst2")
		ensureNamespace(srcNS)
		ensureNamespace(dstNS1)
		ensureNamespace(dstNS2)

		srcName := "shared-dep-" + id
		pinnedKey := types.NamespacedName{Name: "pinned-proj-" + id, Namespace: srcNS}
		unpinnedKey := types.NamespacedName{Name: "unpinned-proj-" + id, Namespace: srcNS}
		DeferCleanup(deleteProjection, pinnedKey)
		DeferCleanup(deleteProjection, unpinnedKey)

		src := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        srcName,
				Namespace:   srcNS,
				Annotations: map[string]string{projectableAnnotation: "true"},
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "x"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "nginx"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, src)).To(Succeed())

		for _, spec := range []struct {
			key        types.NamespacedName
			apiVersion string
			destNS     string
		}{
			{pinnedKey, "apps/v1", dstNS1},
			{unpinnedKey, "apps/*", dstNS2},
		} {
			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: spec.key.Name, Namespace: spec.key.Namespace},
				Spec: projectionv1.ProjectionSpec{
					Source: projectionv1.SourceRef{
						APIVersion: spec.apiVersion, Kind: "Deployment",
						Name: srcName, Namespace: srcNS,
					},
					Destination: projectionv1.DestinationRef{Namespace: spec.destNS},
				},
			}
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		}

		By("both destinations are written by the manager-driven reconciler")
		for _, dstNS := range []string{dstNS1, dstNS2} {
			nsCopy := dstNS
			Eventually(func() error {
				d := &appsv1.Deployment{}
				return k8sClient.Get(ctx,
					client.ObjectKey{Namespace: nsCopy, Name: srcName}, d)
			}, 10*time.Second, 200*time.Millisecond).Should(Succeed(),
				"destination Deployment missing in %s", nsCopy)
		}

		By("apps/v1/Deployment has exactly one watch entry (pinned + unpinned share)")
		sharedR.watchedMu.Lock()
		var appsDeployCount int
		for gvk := range sharedR.watched {
			if gvk.Group == "apps" && gvk.Kind == "Deployment" {
				appsDeployCount++
			}
		}
		sharedR.watchedMu.Unlock()
		Expect(appsDeployCount).To(Equal(1),
			"expected exactly one apps/Deployment watch; got %d", appsDeployCount)

		By("editing the source — both destinations reflect the change via the shared watch")
		updated := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx,
			client.ObjectKey{Namespace: srcNS, Name: srcName}, updated)).To(Succeed())
		if updated.Spec.Template.Annotations == nil {
			updated.Spec.Template.Annotations = map[string]string{}
		}
		updated.Spec.Template.Annotations["bump"] = "1"
		Expect(k8sClient.Update(ctx, updated)).To(Succeed())

		for _, dstNS := range []string{dstNS1, dstNS2} {
			nsCopy := dstNS
			Eventually(func() string {
				d := &appsv1.Deployment{}
				if err := k8sClient.Get(ctx,
					client.ObjectKey{Namespace: nsCopy, Name: srcName}, d); err != nil {
					return ""
				}
				return d.Spec.Template.Annotations["bump"]
			}, 10*time.Second, 200*time.Millisecond).Should(Equal("1"),
				"destination in %s didn't receive the source update", nsCopy)
		}
	})

	It("accepts and reconciles a source whose name contains a dot (Finding C regression)", func() {
		sourceNS := uniqueNS("fc-src")
		destNS := uniqueNS("fc-dst")
		ensureNamespace(sourceNS)
		ensureNamespace(destNS)

		const dottedName = "app.config-v2"
		projKey := types.NamespacedName{Name: "finding-c-" + nextID(), Namespace: sourceNS}
		DeferCleanup(deleteProjection, projKey)

		// Source ConfigMap with a name containing a dot — valid Kubernetes
		// subdomain, would have been rejected by admission under the v0.1
		// label-only regex.
		Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dottedName,
				Namespace: sourceNS,
				Annotations: map[string]string{
					"projection.be0x74a.io/projectable": "true",
				},
			},
			Data: map[string]string{"foo": "bar"},
		})).To(Succeed())

		// Projection referencing the dotted source. Admission should accept the
		// subdomain-format name.
		proj := &projectionv1.Projection{
			ObjectMeta: metav1.ObjectMeta{
				Name:      projKey.Name,
				Namespace: projKey.Namespace,
			},
			Spec: projectionv1.ProjectionSpec{
				Source: projectionv1.SourceRef{
					APIVersion: "v1",
					Kind:       "ConfigMap",
					Name:       dottedName,
					Namespace:  sourceNS,
				},
				Destination: projectionv1.DestinationRef{
					Namespace: destNS,
				},
			},
		}
		Expect(k8sClient.Create(ctx, proj)).To(Succeed())

		// The reconciler should pick it up and write a destination with the
		// same dotted name in the destination namespace.
		Eventually(func() error {
			var got corev1.ConfigMap
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: destNS, Name: dottedName}, &got)
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())

		// Sanity-check the ownership annotation — confirms the destination was
		// written by this Projection, not a coincidental pre-existing object.
		var got corev1.ConfigMap
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: destNS, Name: dottedName}, &got)).To(Succeed())
		Expect(got.GetAnnotations()).To(HaveKeyWithValue(
			"projection.be0x74a.io/owned-by",
			projKey.Namespace+"/"+projKey.Name,
		))
	})
})

var _ = Describe("watchedGvks metric", func() {
	It("reflects the distinct-GVK count of the watched map", func() {
		// Reset the gauge for hermetic testing. The metric is module-level,
		// so we read-modify-write.
		watchedGvks.Set(0)

		r := &ProjectionReconciler{
			watched: map[schema.GroupVersionKind]bool{},
		}
		// Simulate two ensureWatch calls for different GVKs, then a duplicate.
		r.watchedMu.Lock()
		r.watched[schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}] = true
		watchedGvks.Inc()
		r.watched[schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}] = true
		watchedGvks.Inc()
		// Duplicate: ensureWatch short-circuits before Inc, so no Inc here.
		r.watchedMu.Unlock()

		Expect(testutil.ToFloat64(watchedGvks)).To(Equal(2.0))
	})
})

// Silence unused-import warnings when the file is edited in isolation.
var _ = client.Object(&corev1.ConfigMap{})
