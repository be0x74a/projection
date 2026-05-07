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
	"sync/atomic"
	"time"

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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// cpCounter is a separate atomic so cluster-projection test names don't
// collide with the namespaced test suite's nsCounter.
var cpCounter uint64

func uniqueCPName(prefix string) string {
	n := atomic.AddUint64(&cpCounter, 1)
	return fmt.Sprintf("%s-%d", prefix, n)
}

// newClusterReconciler builds a ClusterProjectionReconciler wired to the
// envtest cluster — sibling of newReconciler().
func newClusterReconciler() *ClusterProjectionReconciler {
	httpClient, err := rest.HTTPClientFor(cfg)
	Expect(err).NotTo(HaveOccurred())
	mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	Expect(err).NotTo(HaveOccurred())
	dynClient, err := dynamic.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred())

	return &ClusterProjectionReconciler{
		ControllerDeps: &ControllerDeps{
			Client:        k8sClient,
			Scheme:        k8sClient.Scheme(),
			DynamicClient: dynClient,
			RESTMapper:    mapper,
			Recorder:      events.NewFakeRecorder(64),
		},
		// Use a tiny default for unit-test paths so concurrency tweaks
		// in individual tests are obvious.
		SelectorWriteConcurrency: 4,
	}
}

// reconcileClusterOnce runs Reconcile exactly once against the given key.
func reconcileClusterOnce(r *ClusterProjectionReconciler, key types.NamespacedName) reconcile.Result {
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	Expect(err).NotTo(HaveOccurred())
	return res
}

// deleteClusterProjection forcibly removes a ClusterProjection, stripping
// finalizers if needed.
func deleteClusterProjection(name string) {
	cp := &projectionv1.ClusterProjection{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, cp); err != nil {
		return
	}
	_ = k8sClient.Delete(ctx, cp)
	fresh := &projectionv1.ClusterProjection{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, fresh); err == nil {
		fresh.Finalizers = nil
		_ = k8sClient.Update(ctx, fresh)
	}
}

var _ = Describe("ClusterProjection Controller (integration)", func() {
	var r *ClusterProjectionReconciler

	BeforeEach(func() {
		r = newClusterReconciler()
	})

	Context("Explicit namespaces list", func() {
		It("writes the destination into every listed namespace and counts correctly", func() {
			cpName := uniqueCPName("cp-explicit")
			srcNS := uniqueNS("cp-src")
			ns1 := uniqueNS("cp-tgt")
			ns2 := uniqueNS("cp-tgt")
			ns3 := uniqueNS("cp-tgt")

			ensureNamespace(srcNS)
			ensureNamespace(ns1)
			ensureNamespace(ns2)
			ensureNamespace(ns3)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"k": "v"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{ns1, ns2, ns3},
						Name:       "mirrored-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			before := testutil.ToFloat64(reconcileTotal.WithLabelValues(kindClusterProjection, resultSuccess))
			beforeHist := histogramSampleCount(e2eSeconds, kindClusterProjection, eventCreate)
			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})
			Expect(testutil.ToFloat64(reconcileTotal.WithLabelValues(kindClusterProjection, resultSuccess))-before).
				To(BeNumerically(">=", 1), "success counter should have incremented for kind=ClusterProjection")
			// One e2e observation per per-namespace successful Create — three
			// namespaces in this explicit-list spec means delta == 3. Equality
			// (not >=) catches a regression that double-counts (e.g. observing
			// on Update too) or undercounts (e.g. observing once per reconcile
			// rather than once per destination).
			Expect(histogramSampleCount(e2eSeconds, kindClusterProjection, eventCreate)-beforeHist).
				To(BeEquivalentTo(uint64(3)),
					"e2e histogram should have observed exactly one sample per per-namespace create for kind=ClusterProjection,event=create")

			// Every targeted namespace has the destination with our ownership stamps.
			for _, ns := range []string{ns1, ns2, ns3} {
				dst := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "mirrored-cm", Namespace: ns}, dst)).To(Succeed(),
					"destination missing in %s", ns)
				Expect(dst.Data).To(Equal(map[string]string{"k": "v"}))
				Expect(dst.Annotations).To(HaveKeyWithValue(ownedByClusterAnnotation, cpName))
				Expect(dst.Labels).To(HaveKey(ownedByClusterUIDLabel))
				// Cluster-tier ownership keys must be present; namespaced
				// ownership keys must NOT (a stranger projection can't
				// claim a cluster-owned destination on cleanup).
				Expect(dst.Annotations).NotTo(HaveKey(ownedByAnnotation))
				Expect(dst.Labels).NotTo(HaveKey(ownedByUIDLabel))
			}

			// Status counts.
			fresh := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, fresh)).To(Succeed())
			Expect(fresh.Status.NamespacesWritten).To(BeEquivalentTo(3))
			Expect(fresh.Status.NamespacesFailed).To(BeEquivalentTo(0))
			Expect(fresh.Status.DestinationName).To(Equal("mirrored-cm"))

			ready := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("NamespaceSelector matching N namespaces", func() {
		It("writes the destination into all matched namespaces", func() {
			cpName := uniqueCPName("cp-selector")
			srcNS := uniqueNS("cp-src")
			matchNS1 := uniqueNS("cp-match")
			matchNS2 := uniqueNS("cp-match")
			ignoreNS := uniqueNS("cp-ignore")

			ensureNamespace(srcNS)
			labelKey := uniqueCPName("env") // unique to avoid cross-test pollution
			labelVal := "selector"

			ensureNamespaceWithLabels(matchNS1, map[string]string{labelKey: labelVal})
			ensureNamespaceWithLabels(matchNS2, map[string]string{labelKey: labelVal})
			ensureNamespaceWithLabels(ignoreNS, map[string]string{labelKey: "other"})

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"key": "value"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{labelKey: labelVal},
						},
						Name: "selector-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			for _, ns := range []string{matchNS1, matchNS2} {
				dst := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "selector-cm", Namespace: ns}, dst)).To(Succeed(),
					"destination missing in matched namespace %s", ns)
				Expect(dst.Data).To(Equal(map[string]string{"key": "value"}))
			}
			// Non-matching namespace must NOT receive the destination.
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "selector-cm", Namespace: ignoreNS}, &corev1.ConfigMap{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "non-matching namespace should not receive destination")

			fresh := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, fresh)).To(Succeed())
			Expect(fresh.Status.NamespacesWritten).To(BeEquivalentTo(2))
			Expect(fresh.Status.NamespacesFailed).To(BeEquivalentTo(0))
		})
	})

	Context("Partial failure", func() {
		It("counts the failed namespace and surfaces the failure in the condition", func() {
			cpName := uniqueCPName("cp-partial")
			srcNS := uniqueNS("cp-src")
			okNS := uniqueNS("cp-ok")
			conflictNS := uniqueNS("cp-conflict")

			ensureNamespace(srcNS)
			ensureNamespace(okNS)
			ensureNamespace(conflictNS)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"shared": "data"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			// Pre-create a stranger-owned ConfigMap in conflictNS so the
			// fan-out write hits a DestinationConflict there.
			stranger := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "shared-cm",
					Namespace:   conflictNS,
					Annotations: map[string]string{"random-owner": "stranger"},
				},
				Data: map[string]string{"shared": "stranger"},
			}
			Expect(k8sClient.Create(ctx, stranger)).To(Succeed())

			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{okNS, conflictNS},
						Name:       "shared-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			// okNS gets the destination.
			ok := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "shared-cm", Namespace: okNS}, ok)).To(Succeed())
			Expect(ok.Data).To(Equal(map[string]string{"shared": "data"}))

			// conflictNS keeps the stranger.
			confl := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "shared-cm", Namespace: conflictNS}, confl)).To(Succeed())
			Expect(confl.Data).To(Equal(map[string]string{"shared": "stranger"}))

			fresh := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, fresh)).To(Succeed())
			Expect(fresh.Status.NamespacesWritten).To(BeEquivalentTo(1))
			Expect(fresh.Status.NamespacesFailed).To(BeEquivalentTo(1))

			dw := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionDestinationWritten)
			Expect(dw).NotTo(BeNil())
			Expect(dw.Status).To(Equal(metav1.ConditionFalse))
			Expect(dw.Message).To(ContainSubstring(conflictNS),
				"failure message should name the failed namespace, got: %q", dw.Message)
		})
	})

	Context("Stale namespace cleanup", func() {
		It("deletes destinations in namespaces that no longer match the selector", func() {
			cpName := uniqueCPName("cp-stale")
			srcNS := uniqueNS("cp-src")
			ns1 := uniqueNS("cp-stale1")
			ns2 := uniqueNS("cp-stale2")

			ensureNamespace(srcNS)
			labelKey := uniqueCPName("zone")
			ensureNamespaceWithLabels(ns1, map[string]string{labelKey: "in"})
			ensureNamespaceWithLabels(ns2, map[string]string{labelKey: "in"})

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"a": "b"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{labelKey: "in"},
						},
						Name: "stale-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			By("first reconcile — both namespaces receive the destination")
			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})
			for _, ns := range []string{ns1, ns2} {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "stale-cm", Namespace: ns}, &corev1.ConfigMap{})).
					To(Succeed())
			}

			By("flipping ns2's label so the selector no longer matches")
			ns2Obj := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns2}, ns2Obj)).To(Succeed())
			ns2Obj.Labels[labelKey] = "out"
			Expect(k8sClient.Update(ctx, ns2Obj)).To(Succeed())

			By("second reconcile — ns2's destination is removed; ns1's stays")
			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "stale-cm", Namespace: ns2}, &corev1.ConfigMap{})
				return apierrors.IsNotFound(err)
			}, 2*time.Second, 100*time.Millisecond).Should(BeTrue(), "stale destination should be cleaned up")

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "stale-cm", Namespace: ns1}, &corev1.ConfigMap{})).
				To(Succeed())

			fresh := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, fresh)).To(Succeed())
			Expect(fresh.Status.NamespacesWritten).To(BeEquivalentTo(1))
		})
	})

	Context("Source change propagation", func() {
		It("updates every destination when the source object changes", func() {
			cpName := uniqueCPName("cp-srcprop")
			srcNS := uniqueNS("cp-src")
			ns1 := uniqueNS("cp-prop")
			ns2 := uniqueNS("cp-prop")

			ensureNamespace(srcNS)
			ensureNamespace(ns1)
			ensureNamespace(ns2)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"v": "1"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{ns1, ns2},
						Name:       "prop-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			By("updating the source")
			fresh := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: src.Name, Namespace: srcNS}, fresh)).To(Succeed())
			fresh.Data["v"] = "2"
			fresh.Data["new"] = "added"
			Expect(k8sClient.Update(ctx, fresh)).To(Succeed())

			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			for _, ns := range []string{ns1, ns2} {
				dst := &corev1.ConfigMap{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "prop-cm", Namespace: ns}, dst)).To(Succeed())
				Expect(dst.Data).To(Equal(map[string]string{"v": "2", "new": "added"}))
			}
		})
	})

	Context("Source GVR resolution failure", func() {
		It("surfaces SourceResolutionFailed and creates no destinations when the Kind is unknown", func() {
			cpName := uniqueCPName("cp-unknown-kind")
			ns1 := uniqueNS("cp-unknown")
			ns2 := uniqueNS("cp-unknown")

			ensureNamespace(ns1)
			ensureNamespace(ns2)

			// Reference a Kind the RESTMapper doesn't know about. resolveGVR
			// must surface this as SourceResolutionFailed without ever
			// attempting a destination write.
			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Group: "imaginary.example.com", Version: "v1",
						Kind: "FleetOfUnicorns",
						Name: "nope", Namespace: "nope",
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{ns1, ns2},
						Name:       "ghost-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			fresh := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, fresh)).To(Succeed())

			sr := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionSourceResolved)
			Expect(sr).NotTo(BeNil())
			Expect(sr.Status).To(Equal(metav1.ConditionFalse))
			Expect(sr.Reason).To(Equal("SourceResolutionFailed"))

			ready := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))

			// No destination ConfigMap should have been written in any namespace.
			for _, ns := range []string{ns1, ns2} {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "ghost-cm", Namespace: ns}, &corev1.ConfigMap{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(),
					"no destination should have been written in %s", ns)
			}
		})
	})

	Context("Source 404 with cleanup across namespaces", func() {
		It("removes every destination across all targeted namespaces when the source disappears", func() {
			cpName := uniqueCPName("cp-srcdel")
			srcNS := uniqueNS("cp-srcdel-src")
			ns1 := uniqueNS("cp-srcdel-tgt")
			ns2 := uniqueNS("cp-srcdel-tgt")
			ns3 := uniqueNS("cp-srcdel-tgt")

			ensureNamespace(srcNS)
			ensureNamespace(ns1)
			ensureNamespace(ns2)
			ensureNamespace(ns3)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"k": "v"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{ns1, ns2, ns3},
						Name:       "fanout-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			By("first reconcile — destinations exist in every targeted namespace")
			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})
			for _, ns := range []string{ns1, ns2, ns3} {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fanout-cm", Namespace: ns}, &corev1.ConfigMap{})).
					To(Succeed(), "destination missing in %s after first reconcile", ns)
			}

			By("deleting the source object")
			Expect(k8sClient.Delete(ctx, src)).To(Succeed())

			By("second reconcile — source 404 triggers cluster-wide cleanup")
			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			// All destinations should be cleaned up — exercises
			// deleteAllClusterOwnedDestinations through
			// handleClusterSourceFetchError.
			for _, ns := range []string{ns1, ns2, ns3} {
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "fanout-cm", Namespace: ns}, &corev1.ConfigMap{})
					return apierrors.IsNotFound(err)
				}, 2*time.Second, 100*time.Millisecond).Should(BeTrue(),
					"destination should be removed from %s after source deletion", ns)
			}

			fresh := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, fresh)).To(Succeed())
			sr := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionSourceResolved)
			Expect(sr).NotTo(BeNil())
			Expect(sr.Status).To(Equal(metav1.ConditionFalse))
			Expect(sr.Reason).To(Equal("SourceDeleted"))
		})
	})

	Context("Empty namespace selector footgun", func() {
		It("rejects a ClusterProjection with namespaceSelector: {} and writes no destinations", func() {
			cpName := uniqueCPName("cp-empty-sel")
			srcNS := uniqueNS("cp-empty-sel-src")
			otherNS := uniqueNS("cp-empty-sel-other")

			ensureNamespace(srcNS)
			ensureNamespace(otherNS)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"k": "v"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			// namespaceSelector: {} (empty matchLabels and matchExpressions)
			// satisfies CEL admission (`has(self.namespaceSelector)` is true)
			// but resolveTargetNamespaces refuses to fan out across the
			// entire cluster.
			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						NamespaceSelector: &metav1.LabelSelector{},
						Name:              "should-never-exist",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			fresh := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, fresh)).To(Succeed())

			ready := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal("TargetResolutionFailed"))

			dw := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionDestinationWritten)
			Expect(dw).NotTo(BeNil())
			Expect(dw.Status).To(Equal(metav1.ConditionFalse))
			Expect(dw.Message).To(ContainSubstring("entire cluster"),
				"failure message should explain the empty-selector veto, got: %q", dw.Message)

			Expect(fresh.Status.NamespacesWritten).To(BeEquivalentTo(0))
			Expect(fresh.Status.NamespacesFailed).To(BeEquivalentTo(0))

			// No ConfigMap of that name anywhere — including in the source namespace.
			for _, ns := range []string{srcNS, otherNS} {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "should-never-exist", Namespace: ns}, &corev1.ConfigMap{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(),
					"empty-selector ClusterProjection must not write into %s", ns)
			}
		})
	})

	Context("Source opt-out with projectable=false", func() {
		It("garbage-collects every destination and surfaces SourceOptedOut", func() {
			cpName := uniqueCPName("cp-optout")
			srcNS := uniqueNS("cp-optout-src")
			ns1 := uniqueNS("cp-optout-tgt")
			ns2 := uniqueNS("cp-optout-tgt")

			ensureNamespace(srcNS)
			ensureNamespace(ns1)
			ensureNamespace(ns2)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"k": "v"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{ns1, ns2},
						Name:       "optout-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())
			DeferCleanup(deleteClusterProjection, cpName)

			By("first reconcile — destinations are written")
			r.SourceMode = SourceModePermissive
			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})
			for _, ns := range []string{ns1, ns2} {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "optout-cm", Namespace: ns}, &corev1.ConfigMap{})).
					To(Succeed(), "destination missing in %s", ns)
			}

			By("source owner flips projectable=false to opt out")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: src.Name, Namespace: srcNS}, src)).To(Succeed())
			src.Annotations[projectableAnnotation] = "false"
			Expect(k8sClient.Update(ctx, src)).To(Succeed())

			By("second reconcile — destinations are GC'd cluster-wide and status reflects SourceOptedOut")
			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})

			for _, ns := range []string{ns1, ns2} {
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "optout-cm", Namespace: ns}, &corev1.ConfigMap{})
					return apierrors.IsNotFound(err)
				}, 2*time.Second, 100*time.Millisecond).Should(BeTrue(),
					"destination should be removed from %s after opt-out", ns)
			}

			fresh := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, fresh)).To(Succeed())

			sr := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionSourceResolved)
			Expect(sr).NotTo(BeNil())
			Expect(sr.Status).To(Equal(metav1.ConditionFalse))
			Expect(sr.Reason).To(Equal("SourceOptedOut"))

			dw := apimeta.FindStatusCondition(fresh.Status.Conditions, conditionDestinationWritten)
			Expect(dw).NotTo(BeNil())
			// Source resolution failed (opted out), so destination write is
			// surfaced as Unknown via failClusterSource.
			Expect(dw.Status).To(Equal(metav1.ConditionUnknown))
			Expect(dw.Reason).To(Equal("SourceNotResolved"))
		})
	})

	Context("Namespace event mapping (mapNamespace)", func() {
		It("only enqueues explicit-list ClusterProjections that target the event namespace", func() {
			// Two CPs with disjoint explicit namespace lists. A namespace
			// event for cp1's list must not enqueue cp2 (and vice versa).
			cp1Name := uniqueCPName("cp-ns-evt-a")
			cp2Name := uniqueCPName("cp-ns-evt-b")
			ns1 := uniqueNS("cp-ns-evt-a")
			ns2 := uniqueNS("cp-ns-evt-b")

			cp1 := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cp1Name},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: "irrelevant", Namespace: "irrelevant",
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{ns1},
					},
				},
			}
			cp2 := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cp2Name},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: "irrelevant", Namespace: "irrelevant",
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{ns2},
					},
				},
			}

			items := []projectionv1.ClusterProjection{*cp1, *cp2}

			toNames := func(reqs []reconcile.Request) []string {
				out := make([]string, 0, len(reqs))
				for _, req := range reqs {
					out = append(out, req.Name)
				}
				return out
			}

			// Event for ns1: only cp1 should be enqueued.
			names := toNames(r.matchingClusterProjectionRequests(items, ns1, nil))
			Expect(names).To(ConsistOf(cp1Name),
				"namespace event for %q should only enqueue the CP that targets it, got %v", ns1, names)

			// Event for ns2: only cp2 should be enqueued.
			names = toNames(r.matchingClusterProjectionRequests(items, ns2, nil))
			Expect(names).To(ConsistOf(cp2Name),
				"namespace event for %q should only enqueue the CP that targets it, got %v", ns2, names)

			// Event for an unrelated namespace: nothing should be enqueued.
			Expect(r.matchingClusterProjectionRequests(items, "unrelated-ns", nil)).To(BeEmpty(),
				"namespace event for an unrelated namespace must not enqueue any explicit-list CP")
		})
	})

	Context("Finalizer cleanup on ClusterProjection delete", func() {
		It("removes every owned destination across all namespaces before the finalizer is stripped", func() {
			cpName := uniqueCPName("cp-finalizer")
			srcNS := uniqueNS("cp-src")
			ns1 := uniqueNS("cp-fin")
			ns2 := uniqueNS("cp-fin")
			ns3 := uniqueNS("cp-fin")

			ensureNamespace(srcNS)
			ensureNamespace(ns1)
			ensureNamespace(ns2)
			ensureNamespace(ns3)

			src := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "src-cm",
					Namespace:   srcNS,
					Annotations: map[string]string{projectableAnnotation: "true"},
				},
				Data: map[string]string{"x": "y"},
			}
			Expect(k8sClient.Create(ctx, src)).To(Succeed())

			cp := &projectionv1.ClusterProjection{
				ObjectMeta: metav1.ObjectMeta{Name: cpName},
				Spec: projectionv1.ClusterProjectionSpec{
					Source: projectionv1.SourceRef{
						Version: "v1", Kind: "ConfigMap",
						Name: src.Name, Namespace: srcNS,
					},
					Destination: projectionv1.ClusterProjectionDestination{
						Namespaces: []string{ns1, ns2, ns3},
						Name:       "fin-cm",
					},
				},
			}
			Expect(k8sClient.Create(ctx, cp)).To(Succeed())

			By("first reconcile — destinations exist")
			reconcileClusterOnce(r, types.NamespacedName{Name: cpName})
			for _, ns := range []string{ns1, ns2, ns3} {
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fin-cm", Namespace: ns}, &corev1.ConfigMap{})).
					To(Succeed())
			}

			By("deleting the ClusterProjection")
			live := &projectionv1.ClusterProjection{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, live)).To(Succeed())
			// Confirm the cluster finalizer was added before deletion.
			Expect(live.Finalizers).To(ContainElement(clusterFinalizerName))
			Expect(k8sClient.Delete(ctx, live)).To(Succeed())

			By("reconcile finds DeletionTimestamp and runs cleanup")
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: cpName}})
			Expect(err).NotTo(HaveOccurred())

			// All destinations gone.
			for _, ns := range []string{ns1, ns2, ns3} {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: "fin-cm", Namespace: ns}, &corev1.ConfigMap{})
				Expect(apierrors.IsNotFound(err)).To(BeTrue(),
					"destination should be removed in %s", ns)
			}
			// The CR itself should be GC'd once the finalizer is removed.
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: cpName}, &projectionv1.ClusterProjection{})
				return apierrors.IsNotFound(err)
			}).Should(BeTrue())
		})
	})
})

// ensureNamespaceWithLabels is the labeled sibling of ensureNamespace.
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

// Manager-driven cluster reconciler tests — rely on the watch
// infrastructure (ensureSourceWatch, ensureDestWatch, namespace watch)
// rather than direct Reconcile calls.
//
// This block uses its own dedicated manager so the cluster reconciler can
// run unmolested. Only the cluster reconciler is registered (no
// namespaced one) because controller-runtime keeps a process-global
// registry of controller names — registering a second
// "projection" controller would collide with the one in the namespaced
// "Shared source watch (integration with manager)" test block.
var _ = Describe("ClusterProjection self-healing watches (integration with manager)", Ordered, func() {
	var (
		mgr            ctrl.Manager
		mgrCancel      context.CancelFunc
		clusterCounter uint64
	)

	BeforeAll(func() {
		var err error
		mgr, err = ctrl.NewManager(cfg, ctrl.Options{
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

		// Long RequeueInterval so any catch-up reconcile in tests has to
		// come from the dest-watch / source-watch / namespace-watch
		// pipelines, not from periodic resync.
		const longInterval = 5 * time.Minute

		clusterRecon := &ClusterProjectionReconciler{
			ControllerDeps: &ControllerDeps{
				Client:        mgr.GetClient(),
				Scheme:        mgr.GetScheme(),
				DynamicClient: dynClient,
				RESTMapper:    mapper,
				Recorder:      events.NewFakeRecorder(256),
			},
			RequeueInterval:          longInterval,
			SelectorWriteConcurrency: 4,
		}
		Expect(clusterRecon.SetupWithManager(mgr)).To(Succeed())

		var mgrCtx context.Context
		mgrCtx, mgrCancel = context.WithCancel(context.Background())
		go func() {
			defer GinkgoRecover()
			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
		Expect(mgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())
	})

	AfterAll(func() {
		if mgrCancel != nil {
			mgrCancel()
		}
	})

	It("recreates a destination ConfigMap that was manually deleted (cluster ensureDestWatch)", func() {
		n := atomic.AddUint64(&clusterCounter, 1)
		cpName := fmt.Sprintf("cp-self-heal-%d", n)
		srcNS := uniqueNS("cp-self-heal-src")
		dstNS1 := uniqueNS("cp-self-heal-dst")
		dstNS2 := uniqueNS("cp-self-heal-dst")

		ensureNamespace(srcNS)
		ensureNamespace(dstNS1)
		ensureNamespace(dstNS2)

		Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "src-cm",
				Namespace:   srcNS,
				Annotations: map[string]string{projectableAnnotation: "true"},
			},
			Data: map[string]string{"k": "v"},
		})).To(Succeed())

		Expect(k8sClient.Create(ctx, &projectionv1.ClusterProjection{
			ObjectMeta: metav1.ObjectMeta{Name: cpName},
			Spec: projectionv1.ClusterProjectionSpec{
				Source: projectionv1.SourceRef{
					Version: "v1", Kind: "ConfigMap",
					Name: "src-cm", Namespace: srcNS,
				},
				Destination: projectionv1.ClusterProjectionDestination{
					Namespaces: []string{dstNS1, dstNS2},
					Name:       "dst-cm",
				},
			},
		})).To(Succeed())
		DeferCleanup(deleteClusterProjection, cpName)

		// Wait for the manager-driven reconciler to write both destinations.
		for _, ns := range []string{dstNS1, dstNS2} {
			nsCopy := ns
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Name: "dst-cm", Namespace: nsCopy}, &corev1.ConfigMap{})
			}, 10*time.Second, 200*time.Millisecond).Should(Succeed(),
				"destination missing in %s", ns)
		}

		// Manually delete one destination.
		victim := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "dst-cm", Namespace: dstNS1}, victim)).To(Succeed())
		Expect(k8sClient.Delete(ctx, victim)).To(Succeed())

		// ensureDestWatch should fire a reconcile that recreates the
		// destination within ~2s. RequeueInterval is 5m so a periodic
		// resync is ruled out.
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: "dst-cm", Namespace: dstNS1}, &corev1.ConfigMap{})
		}, 4*time.Second, 100*time.Millisecond).Should(Succeed(),
			"ensureDestWatch should have recreated the destination")
	})
})
