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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/be0x74a/projection/test/utils"
)

const (
	operatorNamespace = "projection-system"
	// operatorSelector matches the controller pod regardless of how it was
	// installed: Kustomize names the Deployment "projection-controller-manager",
	// Helm uses the release name. Both label the pod control-plane=controller-manager.
	operatorSelector = "control-plane=controller-manager"

	ownedByAnnotation = "projection.be0x74a.io/owned-by"

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
			projNS := srcNS
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
    projection.be0x74a.io/projectable: "true"
data:
  color: blue
  shape: square
`, cmName, srcNS))).To(Succeed())

			By("creating the Projection")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: %s
    namespace: %s
  destination:
    namespace: %s
`, projName, projNS, cmName, srcNS, dstNS))).To(Succeed())

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
    projection.be0x74a.io/projectable: "true"
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
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    apiVersion: v1
    kind: Service
    name: %s
    namespace: %s
  destination:
    namespace: %s
`, projName, srcNS, svcName, srcNS, dstNS))).To(Succeed())

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
					"projection", projName, "-n", srcNS)
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
    projection.be0x74a.io/projectable: "true"
data:
  origin: projection
`, cmName, srcNS))).To(Succeed())

			By("creating a Projection that targets the conflicting destination")
			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: %s
    namespace: %s
  destination:
    namespace: %s
`, projName, srcNS, cmName, srcNS, dstNS))).To(Succeed())

			By("waiting for DestinationWritten=False with reason DestinationConflict")
			Eventually(func(g Gomega) {
				reason := getJSONPath(
					`{.status.conditions[?(@.type=="DestinationWritten")].reason}`,
					"projection", projName, "-n", srcNS)
				status := getJSONPath(
					`{.status.conditions[?(@.type=="DestinationWritten")].status}`,
					"projection", projName, "-n", srcNS)
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
					"-n", srcNS,
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
    projection.be0x74a.io/projectable: "true"
data:
  version: "1"
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: %s
    namespace: %s
  destination:
    namespace: %s
`, projName, srcNS, cmName, srcNS, dstNS))).To(Succeed())

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
    projection.be0x74a.io/projectable: "true"
data:
  k: v
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: %s
    namespace: %s
  destination:
    namespace: %s
`, projName, srcNS, cmName, srcNS, dstNS))).To(Succeed())

			By("waiting for the destination to appear")
			Eventually(func() string {
				return getJSONPath("{.metadata.name}", "cm", cmName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal(cmName))

			By("deleting the Projection")
			_, err := utils.Run(exec.Command("kubectl", "delete", "projection", projName,
				"-n", srcNS, "--wait=true", "--timeout=30s"))
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
				"-n", srcNS, "--ignore-not-found=true", "-o", "name"))
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
    projection.be0x74a.io/projectable: "true"
data:
  k: v
`, cmName, srcNS))).To(Succeed())

			Expect(kubectlApply(fmt.Sprintf(`apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    name: %s
    namespace: %s
  destination:
    namespace: %s
`, projName, srcNS, cmName, srcNS, dstNS))).To(Succeed())

			By("waiting for the destination to be projected and annotated")
			Eventually(func() string {
				return getJSONPath(fmt.Sprintf("{.metadata.annotations.%s}",
					strings.ReplaceAll(ownedByAnnotation, ".", `\.`)),
					"cm", cmName, "-n", dstNS)
			}, defaultEventually, defaultTick).Should(Equal(srcNS + "/" + projName))

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
				"-n", srcNS, "--wait=true", "--timeout=30s"))
			Expect(err).NotTo(HaveOccurred())

			By("asserting the destination survives (no ownership, no delete)")
			// Give the reconciler a beat to observe the deletion event and no-op.
			Consistently(func() string {
				return getJSONPath("{.metadata.name}", "cm", cmName, "-n", dstNS)
			}, 3*time.Second, 500*time.Millisecond).Should(Equal(cmName))
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
