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
	"fmt"
	"strings"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
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
			ns := uniqueNS("sel-mutex")
			ensureNamespace(ns)

			proj := &projectionv1.Projection{
				ObjectMeta: metav1.ObjectMeta{Name: "p-mutex", Namespace: ns},
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
			Expect(k8sClient.Create(ctx, proj)).To(Succeed())
			projKey := types.NamespacedName{Name: proj.Name, Namespace: proj.Namespace}
			DeferCleanup(func() { deleteProjection(projKey) })

			reconcileOnce(r, projKey)

			var got projectionv1.Projection
			Expect(k8sClient.Get(ctx, projKey, &got)).To(Succeed())
			Expect(got.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", Equal(conditionDestinationWritten)),
				HaveField("Status", Equal(metav1.ConditionFalse)),
				HaveField("Reason", Equal("InvalidSpec")),
				HaveField("Message", ContainSubstring("mutually exclusive")),
			)))
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

// Silence unused-import warnings when the file is edited in isolation.
var _ = client.Object(&corev1.ConfigMap{})
