/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package e2e_upgrade

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/projection-operator/projection/test/utils"
)

const (
	operatorNamespace = "projection-system"
	oldChartRef       = "oci://ghcr.io/be0x74a/charts/projection"
	oldChartVersion   = "0.1.0-alpha.1"
	releaseName       = "projection"

	projectionNS   = "upgrade-e2e-src"
	destNamespace  = "upgrade-e2e-dst"
	sourceName     = "app-config"
	projectionName = "upgrade-e2e"

	projectableAnnotation = "projection.be0x74a.io/projectable"
	ownedByAnnotation     = "projection.be0x74a.io/owned-by"

	eventuallyTimeout = 120 * time.Second
	pollInterval      = 2 * time.Second
)

// repoRoot returns the repo root path by walking up from this file's location.
// Needed so `helm upgrade` against ./charts/projection and `./hack/migrate-to-v1.sh`
// resolve regardless of working directory.
func repoRoot() string {
	_, f, _, _ := runtime.Caller(0)
	// f is .../test/e2e-upgrade/upgrade_test.go; repo root is two dirs up.
	return filepath.Join(filepath.Dir(f), "..", "..")
}

func kubectl(args ...string) (string, error) {
	out, err := utils.Run(exec.Command("kubectl", args...))
	return string(out), err
}

func helm(args ...string) (string, error) {
	out, err := utils.Run(exec.Command("helm", args...))
	return string(out), err
}

func apply(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

var _ = Describe("Upgrade v0.1 -> v0.2", Ordered, func() {

	BeforeAll(func() {
		By("installing chart v0.1.0-alpha.1 from ghcr.io")
		// --create-namespace is used because the release namespace won't exist
		// in a fresh Kind cluster.
		_, err := helm("install", releaseName, oldChartRef,
			"--version", oldChartVersion,
			"--namespace", operatorNamespace,
			"--create-namespace",
			"--wait", "--timeout=180s")
		Expect(err).ToNot(HaveOccurred(), "helm install v0.1.0-alpha.1 failed")

		By("waiting for the v0.1 operator Deployment to be Available")
		Eventually(func() string {
			out, _ := kubectl("get", "deploy", "-n", operatorNamespace,
				"-l", "control-plane=controller-manager",
				"-o", "jsonpath={.items[*].status.availableReplicas}")
			return strings.TrimSpace(out)
		}, eventuallyTimeout, pollInterval).Should(Equal("1"))

		By("creating test namespaces")
		Expect(apply(fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", projectionNS))).To(Succeed())
		Expect(apply(fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", destNamespace))).To(Succeed())
	})

	AfterAll(func() {
		By("uninstalling the chart")
		_, _ = helm("uninstall", releaseName, "-n", operatorNamespace, "--wait", "--ignore-not-found")
		By("deleting test namespaces")
		_, _ = kubectl("delete", "ns", projectionNS, "--ignore-not-found", "--wait=false")
		_, _ = kubectl("delete", "ns", destNamespace, "--ignore-not-found", "--wait=false")
		_, _ = kubectl("delete", "ns", operatorNamespace, "--ignore-not-found", "--wait=false")
	})

	It("preserves Projection state across helm upgrade when migration runs first", func() {
		By("creating a source ConfigMap and a Projection under v0.1")
		srcManifest := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  log_level: info
  feature_flag: enabled
`, sourceName, projectionNS)
		Expect(apply(srcManifest)).To(Succeed())

		projManifest := fmt.Sprintf(`apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: %s
  namespace: %s
spec:
  source:
    apiVersion: v1
    kind: ConfigMap
    namespace: %s
    name: %s
  destination:
    namespace: %s
`, projectionName, projectionNS, projectionNS, sourceName, destNamespace)
		Expect(apply(projManifest)).To(Succeed())

		By("waiting for the Projection to reach Ready=True under v0.1")
		Eventually(func() string {
			out, _ := kubectl("-n", projectionNS, "get", "projection", projectionName,
				"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}")
			return strings.TrimSpace(out)
		}, eventuallyTimeout, pollInterval).Should(Equal("True"))

		By("asserting the destination ConfigMap exists with matching data")
		destData, err := kubectl("-n", destNamespace, "get", "configmap", sourceName,
			"-o", "jsonpath={.data.log_level},{.data.feature_flag}")
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(destData)).To(Equal("info,enabled"))

		destOwnedBy, err := kubectl("-n", destNamespace, "get", "configmap", sourceName,
			"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", strings.ReplaceAll(ownedByAnnotation, ".", "\\.")))
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(destOwnedBy)).To(Equal(fmt.Sprintf("%s/%s", projectionNS, projectionName)))

		By("running the migration script in --apply mode")
		migrate := exec.Command(filepath.Join(repoRoot(), "hack", "migrate-to-v1.sh"), "--apply")
		out, err := utils.Run(migrate)
		Expect(err).ToNot(HaveOccurred(), "migration script failed: %s", out)
		Expect(string(out)).To(ContainSubstring("annotate (projectable=true)"),
			"expected the source to be in the annotate plan")

		By("asserting the source is now annotated projectable=true")
		srcAnnotation, err := kubectl("-n", projectionNS, "get", "configmap", sourceName,
			"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", strings.ReplaceAll(projectableAnnotation, ".", "\\.")))
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(srcAnnotation)).To(Equal("true"))

		By("helm upgrade to the current chart")
		chartPath := filepath.Join(repoRoot(), "charts", "projection")
		_, err = helm("upgrade", releaseName, chartPath,
			"--namespace", operatorNamespace,
			"--reset-values",
			"--set", "image.repository=projection",
			"--set", "image.tag=dev",
			"--set", "image.pullPolicy=Never",
			"--wait", "--timeout=180s")
		Expect(err).ToNot(HaveOccurred(), "helm upgrade to current chart failed")

		By("waiting for the upgraded operator Deployment to be Available")
		Eventually(func() string {
			out, _ := kubectl("get", "deploy", "-n", operatorNamespace,
				"-l", "control-plane=controller-manager",
				"-o", "jsonpath={.items[*].status.availableReplicas}")
			return strings.TrimSpace(out)
		}, eventuallyTimeout, pollInterval).Should(Equal("1"))

		By("asserting the Projection stays Ready=True post-upgrade")
		// Short grace period for the upgraded operator to resync.
		Eventually(func() string {
			out, _ := kubectl("-n", projectionNS, "get", "projection", projectionName,
				"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}")
			return strings.TrimSpace(out)
		}, eventuallyTimeout, pollInterval).Should(Equal("True"))

		By("asserting destination data is preserved")
		destData, err = kubectl("-n", destNamespace, "get", "configmap", sourceName,
			"-o", "jsonpath={.data.log_level},{.data.feature_flag}")
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(destData)).To(Equal("info,enabled"),
			"destination data was modified by the upgrade")

		By("asserting owned-by annotation survived the upgrade")
		destOwnedBy, err = kubectl("-n", destNamespace, "get", "configmap", sourceName,
			"-o", fmt.Sprintf("jsonpath={.metadata.annotations['%s']}", strings.ReplaceAll(ownedByAnnotation, ".", "\\.")))
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(destOwnedBy)).To(Equal(fmt.Sprintf("%s/%s", projectionNS, projectionName)))

		By("editing the source and verifying propagation under the upgraded operator")
		editedSrc := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  annotations:
    %s: "true"
data:
  log_level: info
  feature_flag: enabled
  new_field: hello
`, sourceName, projectionNS, projectableAnnotation)
		Expect(apply(editedSrc)).To(Succeed())

		Eventually(func() string {
			out, _ := kubectl("-n", destNamespace, "get", "configmap", sourceName,
				"-o", "jsonpath={.data.new_field}")
			return strings.TrimSpace(out)
		}, eventuallyTimeout, pollInterval).Should(Equal("hello"),
			"upgraded operator did not propagate source edit to destination")
	})
})
