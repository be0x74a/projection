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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/projection-operator/projection/test/utils"
)

const (
	operatorNamespace = "projection-system"
	// operatorSelector matches the controller pod regardless of how it was
	// installed: Kustomize names the Deployment "projection-controller-manager",
	// Helm uses the release name. Both label the pod control-plane=controller-manager.
	operatorSelector = "control-plane=controller-manager"

	ownedByAnnotation = "projection.sh/owned-by-projection"

	// runID is appended to every resource created by the suite so concurrent
	// runs or leftover artifacts from earlier runs never collide.
	defaultEventually = 30 * time.Second
	defaultTick       = 500 * time.Millisecond
	// watchLatencyBudget is the de-facto SLO for dynamic-watch propagation.
	watchLatencyBudget = 5 * time.Second
)

// counter gives each It a unique suffix so namespaces don't collide across
// Its in the same run (Ginkgo Ordered Describe shares the cluster).
var counter uint32

func nextID() string {
	n := atomic.AddUint32(&counter, 1)
	return fmt.Sprintf("%d-%d", time.Now().Unix()%100000, n)
}

// kubectlApply pipes the given YAML through `kubectl apply -f -`.
func kubectlApply(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

// kubectlDelete is best-effort (used in AfterEach).
func kubectlDelete(args ...string) {
	cmd := exec.Command("kubectl", append([]string{"delete", "--ignore-not-found=true", "--wait=false"}, args...)...)
	_, _ = utils.Run(cmd)
}

func createNamespace(ns string) {
	Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
`, ns))).To(Succeed())
}

// createNamespaceWithHoldFinalizer creates a namespace with a custom
// metadata.finalizer so `kubectl delete ns` leaves it pinned in Terminating
// state until the finalizer is released (via releaseNamespaceFinalizer).
// Used to exercise reconcile paths that encounter a namespace in
// Terminating state.
func createNamespaceWithHoldFinalizer(ns, finalizer string) {
	Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  finalizers:
    - %s
`, ns, finalizer))).To(Succeed())
}

// releaseNamespaceFinalizer strips all metadata.finalizers so the namespace
// can finish terminating. Best-effort — used in DeferCleanup.
func releaseNamespaceFinalizer(ns string) {
	_, _ = utils.Run(exec.Command("kubectl", "patch", "namespace", ns,
		"--type=merge", "-p", `{"metadata":{"finalizers":[]}}`))
}

func getJSONPath(path string, args ...string) string {
	full := append([]string{"get"}, args...)
	full = append(full, "-o", "jsonpath="+path)
	out, err := utils.Run(exec.Command("kubectl", full...))
	if err != nil {
		return ""
	}
	return string(out)
}

var _ = Describe("Projection E2E", Ordered, func() {
	BeforeAll(func() {
		By("verifying an operator deployment is Available in the dev cluster")
		out, err := utils.Run(exec.Command("kubectl", "get", "deploy",
			"-n", operatorNamespace, "-l", operatorSelector,
			"-o", "jsonpath={.items[*].status.availableReplicas}"))
		// jsonpath returns space-separated counts for each matched deployment;
		// at least one must be a positive integer.
		hasAvailable := false
		for _, tok := range strings.Fields(strings.TrimSpace(string(out))) {
			if tok != "" && tok != "0" {
				hasAvailable = true
				break
			}
		}
		if err != nil || !hasAvailable {
			Skip(fmt.Sprintf("no Available operator deployment matching %q in namespace %s (kubectl context=%s). "+
				"Bring up a Kind cluster with the operator deployed (make deploy or helm install) before running e2e.",
				operatorSelector, operatorNamespace, kubectlContext()))
		}
	})

	Context("Mirror ConfigMap across namespaces", func() {
		It("projects a ConfigMap into the destination namespace and marks Ready=True", func() {
			id := nextID()
			srcNS := "e2e-src-" + id
			dstNS := "e2e-dst-" + id
			projNS := dstNS
			projName := "p-cm-" + id
			cmName := "payload-" + id

			createNamespace(srcNS)
			createNamespace(dstNS)
			DeferCleanup(func() {
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			By("creating the source ConfigMap")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
data:
  color: blue
  shape: square
`, cmName, srcNS))).To(Succeed())

			By("creating the Projection")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: %s
    namespace: %s
`, projName, projNS, cmName, srcNS))).To(Succeed())

			By("waiting for destination ConfigMap to appear with matching data")
			Eventually(func(g Gomega) {
				color := getJSONPath("{.data.color}", "cm", cmName, "-n", dstNS)
				shape := getJSONPath("{.data.shape}", "cm", cmName, "-n", dstNS)
				g.Expect(color).To(Equal("blue"))
				g.Expect(shape).To(Equal("square"))
			}, defaultEventually, defaultTick).Should(Succeed())

			By("verifying ownership annotation")
			owner := getJSONPath(fmt.Sprintf("{.metadata.annotations.%s}",
				strings.ReplaceAll(ownedByAnnotation, ".", `\.`)),
				"cm", cmName, "-n", dstNS)
			Expect(owner).To(Equal(projNS + "/" + projName))

			By("verifying Ready=True on the Projection")
			Eventually(func() string {
				return getJSONPath(`{.status.conditions[?(@.type=="Ready")].status}`,
					"projection", projName, "-n", projNS)
			}, defaultEventually, defaultTick).Should(Equal("True"))
		})
	})

	Context("Mirror a Service", func() {
		It("mirrors the Service and strips clusterIP on the destination", func() {
			id := nextID()
			srcNS := "e2e-src-" + id
			dstNS := "e2e-dst-" + id
			projName := "p-svc-" + id
			svcName := "echo-" + id

			createNamespace(srcNS)
			createNamespace(dstNS)
			DeferCleanup(func() {
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			By("creating the source Service")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
spec:
  selector:
    app: echo
  ports:
    - name: http
      port: 80
      targetPort: 8080
`, svcName, srcNS))).To(Succeed())

			By("capturing the source clusterIP (allocated by the apiserver)")
			var srcIP string
			Eventually(func() string {
				srcIP = getJSONPath("{.spec.clusterIP}", "svc", svcName, "-n", srcNS)
				return srcIP
			}, defaultEventually, defaultTick).ShouldNot(Or(BeEmpty(), Equal("None")))

			By("creating the Projection")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: Service
    name: %s
    namespace: %s
`, projName, dstNS, svcName, srcNS))).To(Succeed())

			By("waiting for the destination Service to be created with a fresh clusterIP")
			Eventually(func(g Gomega) {
				dstIP := getJSONPath("{.spec.clusterIP}", "svc", svcName, "-n", dstNS)
				g.Expect(dstIP).NotTo(BeEmpty())
				g.Expect(dstIP).NotTo(Equal(srcIP),
					"destination clusterIP %q must differ from source %q (apiserver allocates on create)",
					dstIP, srcIP)
			}, defaultEventually, defaultTick).Should(Succeed())

			By("verifying Ready=True")
			Eventually(func() string {
				return getJSONPath(`{.status.conditions[?(@.type=="Ready")].status}`,
					"projection", projName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal("True"))
		})
	})

	Context("Conflict detection", func() {
		It("leaves an unowned destination untouched and surfaces DestinationConflict", func() {
			id := nextID()
			srcNS := "e2e-src-" + id
			dstNS := "e2e-dst-" + id
			projName := "p-conflict-" + id
			cmName := "payload-" + id

			createNamespace(srcNS)
			createNamespace(dstNS)
			DeferCleanup(func() {
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			By("pre-creating an unowned ConfigMap in the destination namespace")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    stranger: "yes"
data:
  origin: stranger
`, cmName, dstNS))).To(Succeed())

			By("creating the source ConfigMap with different data")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
data:
  origin: projection
`, cmName, srcNS))).To(Succeed())

			By("creating a Projection that targets the conflicting destination")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: %s
    namespace: %s
`, projName, dstNS, cmName, srcNS))).To(Succeed())

			By("waiting for DestinationWritten=False with reason DestinationConflict")
			Eventually(func(g Gomega) {
				reason := getJSONPath(
					`{.status.conditions[?(@.type=="DestinationWritten")].reason}`,
					"projection", projName, "-n", dstNS)
				status := getJSONPath(
					`{.status.conditions[?(@.type=="DestinationWritten")].status}`,
					"projection", projName, "-n", dstNS)
				g.Expect(status).To(Equal("False"))
				g.Expect(reason).To(Equal("DestinationConflict"))
			}, defaultEventually, defaultTick).Should(Succeed())

			By("confirming the unowned ConfigMap is untouched")
			origin := getJSONPath("{.data.origin}", "cm", cmName, "-n", dstNS)
			Expect(origin).To(Equal("stranger"))
			owner := getJSONPath(fmt.Sprintf("{.metadata.annotations.%s}",
				strings.ReplaceAll(ownedByAnnotation, ".", `\.`)),
				"cm", cmName, "-n", dstNS)
			Expect(owner).To(BeEmpty(), "destination must not have been annotated")

			By("checking for a Warning DestinationConflict event on the Projection")
			Eventually(func() string {
				return getJSONPath(
					`{.items[*].reason}`,
					"events",
					"-n", dstNS,
					"--field-selector",
					fmt.Sprintf("involvedObject.name=%s,type=Warning", projName),
				)
			}, defaultEventually, defaultTick).Should(ContainSubstring("DestinationConflict"))
		})
	})

	Context("Dynamic watch propagation", func() {
		It("propagates a source edit to the destination within the latency budget", func() {
			id := nextID()
			srcNS := "e2e-src-" + id
			dstNS := "e2e-dst-" + id
			projName := "p-watch-" + id
			cmName := "payload-" + id

			createNamespace(srcNS)
			createNamespace(dstNS)
			DeferCleanup(func() {
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
data:
  version: "1"
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: %s
    namespace: %s
`, projName, dstNS, cmName, srcNS))).To(Succeed())

			By("waiting for initial projection")
			Eventually(func() string {
				return getJSONPath("{.data.version}", "cm", cmName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal("1"))

			By("editing the source ConfigMap")
			editedAt := time.Now()
			_, err := utils.Run(exec.Command("kubectl", "patch", "cm", cmName,
				"-n", srcNS, "--type=merge", "-p", `{"data":{"version":"2"}}`))
			Expect(err).NotTo(HaveOccurred())

			By("asserting the destination reflects the change within the watch-latency budget")
			Eventually(func() string {
				return getJSONPath("{.data.version}", "cm", cmName, "-n", dstNS)
			}, watchLatencyBudget, 100*time.Millisecond).Should(Equal("2"))
			_, _ = fmt.Fprintf(GinkgoWriter, "watch propagation observed within %s\n",
				time.Since(editedAt).Round(10*time.Millisecond))
		})
	})

	Context("Deletion cleanup", func() {
		It("deletes the owned destination and releases the Projection finalizer", func() {
			id := nextID()
			srcNS := "e2e-src-" + id
			dstNS := "e2e-dst-" + id
			projName := "p-del-" + id
			cmName := "payload-" + id

			createNamespace(srcNS)
			createNamespace(dstNS)
			DeferCleanup(func() {
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
data:
  k: v
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: %s
    namespace: %s
`, projName, dstNS, cmName, srcNS))).To(Succeed())

			By("waiting for the destination to appear")
			Eventually(func() string {
				return getJSONPath("{.metadata.name}", "cm", cmName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal(cmName))

			By("deleting the Projection")
			_, err := utils.Run(exec.Command("kubectl", "delete", "projection", projName,
				"-n", dstNS, "--wait=true", "--timeout=30s"))
			Expect(err).NotTo(HaveOccurred())

			By("asserting the destination ConfigMap is gone")
			Eventually(func() string {
				out, err := utils.Run(exec.Command("kubectl", "get", "cm", cmName, "-n", dstNS,
					"--ignore-not-found=true", "-o", "name"))
				if err != nil {
					return ""
				}
				return strings.TrimSpace(string(out))
			}, defaultEventually, defaultTick).Should(BeEmpty())

			By("asserting the Projection is fully gone (finalizer released)")
			out, _ := utils.Run(exec.Command("kubectl", "get", "projection", projName,
				"-n", dstNS, "--ignore-not-found=true", "-o", "name"))
			Expect(strings.TrimSpace(string(out))).To(BeEmpty())
		})

		It("leaves the destination alone when ownership was stripped before deletion", func() {
			id := nextID()
			srcNS := "e2e-src-" + id
			dstNS := "e2e-dst-" + id
			projName := "p-del-unowned-" + id
			cmName := "payload-" + id

			createNamespace(srcNS)
			createNamespace(dstNS)
			DeferCleanup(func() {
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
data:
  k: v
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: %s
    namespace: %s
`, projName, dstNS, cmName, srcNS))).To(Succeed())

			By("waiting for the destination to be projected and annotated")
			Eventually(func() string {
				return getJSONPath(fmt.Sprintf("{.metadata.annotations.%s}",
					strings.ReplaceAll(ownedByAnnotation, ".", `\.`)),
					"cm", cmName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal(dstNS + "/" + projName))

			By("stripping the ownership annotation from the destination")
			// JSON-patch with ~1 escape for the '/' in the annotation key.
			jsonKey := strings.ReplaceAll(ownedByAnnotation, "~", "~0")
			jsonKey = strings.ReplaceAll(jsonKey, "/", "~1")
			patch := fmt.Sprintf(`[{"op":"remove","path":"/metadata/annotations/%s"}]`, jsonKey)
			_, err := utils.Run(exec.Command("kubectl", "patch", "cm", cmName,
				"-n", dstNS, "--type=json", "-p", patch))
			Expect(err).NotTo(HaveOccurred())

			By("deleting the Projection")
			_, err = utils.Run(exec.Command("kubectl", "delete", "projection", projName,
				"-n", dstNS, "--wait=true", "--timeout=30s"))
			Expect(err).NotTo(HaveOccurred())

			By("asserting the destination survives (no ownership, no delete)")
			// Give the reconciler a beat to observe the deletion event and no-op.
			Consistently(func() string {
				return getJSONPath("{.metadata.name}", "cm", cmName, "-n", dstNS)
			}, 3*time.Second, 500*time.Millisecond).Should(Equal(cmName))
		})
	})

	Context("Source deletion", func() {
		It("cleans up the destination when the source is deleted and releases the Projection finalizer", func() {
			id := nextID()
			srcNS := "e2e-src-" + id
			dstNS := "e2e-dst-" + id
			projName := "p-srcdel-" + id
			cmName := "payload-" + id

			createNamespace(srcNS)
			createNamespace(dstNS)
			DeferCleanup(func() {
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			By("creating the source ConfigMap and the Projection")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
data:
  k: v
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: %s
    namespace: %s
`, projName, dstNS, cmName, srcNS))).To(Succeed())

			By("waiting for the destination to appear")
			Eventually(func() string {
				return getJSONPath("{.metadata.name}", "cm", cmName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal(cmName))

			By("deleting the source ConfigMap")
			_, err := utils.Run(exec.Command("kubectl", "delete", "configmap", cmName,
				"-n", srcNS, "--wait=true", "--timeout=10s"))
			Expect(err).NotTo(HaveOccurred())

			By("the destination is cleaned up")
			Eventually(func() string {
				out, _ := utils.Run(exec.Command("kubectl", "get", "cm", cmName, "-n", dstNS,
					"--ignore-not-found=true", "-o", "name"))
				return strings.TrimSpace(string(out))
			}, defaultEventually, defaultTick).Should(BeEmpty())

			By("the Projection reports SourceResolved=False reason=SourceDeleted")
			Eventually(func(g Gomega) {
				reason := getJSONPath(
					`{.status.conditions[?(@.type=="SourceResolved")].reason}`,
					"projection", projName, "-n", dstNS)
				status := getJSONPath(
					`{.status.conditions[?(@.type=="SourceResolved")].status}`,
					"projection", projName, "-n", dstNS)
				g.Expect(status).To(Equal("False"))
				g.Expect(reason).To(Equal("SourceDeleted"))
			}, defaultEventually, defaultTick).Should(Succeed())

			By("deleting the Projection — finalizer releases cleanly (no stuck finalizer)")
			_, err = utils.Run(exec.Command("kubectl", "delete", "projection", projName,
				"-n", dstNS, "--wait=true", "--timeout=30s"))
			Expect(err).NotTo(HaveOccurred())

			By("the Projection is fully gone")
			out, _ := utils.Run(exec.Command("kubectl", "get", "projection", projName,
				"-n", dstNS, "--ignore-not-found=true", "-o", "name"))
			Expect(strings.TrimSpace(string(out))).To(BeEmpty())
		})
	})

	Context("Source namespace terminating", func() {
		It("does not break reconcile while the source still exists in a Terminating namespace", func() {
			id := nextID()
			srcNS := "e2e-src-term-" + id
			dstNS := "e2e-dst-" + id
			projName := "p-srcterm-" + id
			cmName := "payload-" + id
			finalizer := "e2e.projection.sh/hold-" + id

			createNamespaceWithHoldFinalizer(srcNS, finalizer)
			createNamespace(dstNS)
			DeferCleanup(func() {
				releaseNamespaceFinalizer(srcNS)
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			By("creating the source ConfigMap and the Projection (both in a Terminating-eligible ns)")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
data:
  k: v
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: %s
    namespace: %s
`, projName, dstNS, cmName, srcNS))).To(Succeed())

			By("waiting for the initial projection to succeed")
			Eventually(func() string {
				return getJSONPath("{.metadata.name}", "cm", cmName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal(cmName))

			By("waiting for Ready=True before putting the source ns into Terminating (avoids racing the controller's status patch)")
			Eventually(func() string {
				return getJSONPath(
					`{.status.conditions[?(@.type=="Ready")].status}`,
					"projection", projName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal("True"))

			By("deleting the source namespace — it will stick in Terminating due to the finalizer")
			_, _ = utils.Run(exec.Command("kubectl", "delete", "namespace", srcNS,
				"--wait=false"))

			By("Projection stays Ready=True while the source is still Get-able (reconcile isn't confused by Terminating ns)")
			Consistently(func() string {
				return getJSONPath(
					`{.status.conditions[?(@.type=="Ready")].status}`,
					"projection", projName, "-n", dstNS)
			}, 3*time.Second, 500*time.Millisecond).Should(Equal("True"))
		})
	})

	Context("Destination namespace terminating", func() {
		It("surfaces DestinationWritten=False reason=DestinationCreateFailed without busy-looping", func() {
			// Under the v0.3.0 namespaced contract, a Projection MUST live
			// in its destination namespace — so a terminating destination
			// rejects the Projection's own apply before the controller ever
			// runs. This scenario is only expressible against
			// ClusterProjection (Projection in one ns, destinations in
			// many). Re-introduce in the cluster-reconciler PR.
			Skip("not expressible against namespaced Projection in v0.3.0; moves to ClusterProjection in a follow-up PR")

			id := nextID()
			srcNS := "e2e-src-" + id
			dstNS := "e2e-dst-term-" + id
			projName := "p-dstterm-" + id
			cmName := "payload-" + id
			finalizer := "e2e.projection.sh/hold-" + id

			createNamespace(srcNS)
			createNamespaceWithHoldFinalizer(dstNS, finalizer)
			DeferCleanup(func() {
				releaseNamespaceFinalizer(dstNS)
				kubectlDelete("ns", srcNS)
				kubectlDelete("ns", dstNS)
			})

			By("putting the destination namespace into Terminating state before the Projection runs")
			_, _ = utils.Run(exec.Command("kubectl", "delete", "namespace", dstNS,
				"--wait=false"))

			By("creating the source and Projection pointing at the Terminating destination ns")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    projection.sh/projectable: "true"
data:
  k: v
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigMap
    name: %s
    namespace: %s
`, projName, dstNS, cmName, srcNS))).To(Succeed())

			By("Projection reports DestinationWritten=False reason=DestinationCreateFailed")
			Eventually(func(g Gomega) {
				status := getJSONPath(
					`{.status.conditions[?(@.type=="DestinationWritten")].status}`,
					"projection", projName, "-n", dstNS)
				reason := getJSONPath(
					`{.status.conditions[?(@.type=="DestinationWritten")].reason}`,
					"projection", projName, "-n", dstNS)
				g.Expect(status).To(Equal("False"))
				g.Expect(reason).To(Equal("DestinationCreateFailed"))
			}, defaultEventually, defaultTick).Should(Succeed())

			By("no tight loop — we don't see hundreds of events for this Projection in a short window")
			Consistently(func() int {
				out, _ := utils.Run(exec.Command("kubectl", "get", "events",
					"-n", dstNS,
					"--field-selector", fmt.Sprintf("involvedObject.name=%s,type=Warning", projName),
					"-o", "jsonpath={.items[*].reason}"))
				return len(strings.Fields(string(out)))
			}, 2*time.Second, 500*time.Millisecond).Should(BeNumerically("<", 50),
				"destination-ns-terminating path emitted an unreasonable number of events; possible busy-loop")
		})
	})

	Context("Non-existent source Kind", func() {
		It("surfaces SourceResolved=False reason=SourceResolutionFailed when the Kind is unknown", func() {
			id := nextID()
			srcNS := "e2e-src-" + id
			projName := "p-badkind-" + id

			createNamespace(srcNS)
			DeferCleanup(func() {
				kubectlDelete("projection", projName, "-n", srcNS)
				kubectlDelete("ns", srcNS)
			})

			By("creating a Projection referencing a Kind the RESTMapper can't resolve")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.sh/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    group: ""
    version: v1
    kind: ConfigNap
    name: nonexistent
    namespace: %s
`, projName, srcNS, srcNS))).To(Succeed())

			By("Projection reports SourceResolved=False reason=SourceResolutionFailed")
			Eventually(func(g Gomega) {
				status := getJSONPath(
					`{.status.conditions[?(@.type=="SourceResolved")].status}`,
					"projection", projName, "-n", srcNS)
				reason := getJSONPath(
					`{.status.conditions[?(@.type=="SourceResolved")].reason}`,
					"projection", projName, "-n", srcNS)
				g.Expect(status).To(Equal("False"))
				g.Expect(reason).To(Equal("SourceResolutionFailed"))
			}, defaultEventually, defaultTick).Should(Succeed())

			By("Projection remains deletable (no stuck finalizer from a reconcile that never completed)")
			_, err := utils.Run(exec.Command("kubectl", "delete", "projection", projName,
				"-n", srcNS, "--wait=true", "--timeout=30s"))
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

// kubectlContext returns the current kubectl context, best-effort.
func kubectlContext() string {
	out, err := utils.Run(exec.Command("kubectl", "config", "current-context"))
	if err != nil {
		return "<unknown>"
	}
	return strings.TrimSpace(string(out))
}
