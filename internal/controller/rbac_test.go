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

// Aggregation strategy: Option A — pre-merged "edit"/"view" ClusterRoles.
//
// Real Kubernetes performs ClusterRole aggregation in kube-controller-
// manager: it watches for the `rbac.authorization.k8s.io/aggregate-to-*`
// labels and rewrites the aggregating role's rules in-place. envtest does
// NOT run kube-controller-manager (only etcd + kube-apiserver are
// downloaded by `setup-envtest`), so the chart's aggregation labels are
// no-ops in this test environment. envtest's apiserver bootstrap creates
// `admin`, `edit`, and `view` as empty stubs that real clusters would
// fill in via aggregation.
//
// To still verify the matrix end-to-end, this test overwrites those stub
// roles with rules sourced from the chart's
// `<release>-projection-namespaced-{edit,view}` ClusterRoles. That
// mirrors what aggregation would produce in a real cluster:
//   - alice binds (via RoleBinding) to `edit` and inherits namespaced
//     Projection edit rights via "aggregation".
//   - bob binds to `view` and inherits namespaced Projection read rights.
//   - carol binds DIRECTLY to `<release>-projection-cluster-admin`
//     because that role is NOT aggregated by design.
//
// The chart's aggregation LABELS themselves are verified separately by
// helm-unittest in PR #79. This test verifies the chart's RULES, once
// aggregated, produce the intended matrix in the real RBAC engine.

import (
	"bytes"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// rbacReleaseName is the helm release name passed when rendering the
// chart in test setup. Together with the chart's name (`projection`),
// the templated `<release>-projection-*` ClusterRoles end up named
// `test-projection-namespaced-edit`, `-namespaced-view`, and
// `-cluster-admin` (see charts/projection/templates/_helpers.tpl).
const rbacReleaseName = "test"

const (
	rbacChartNamespacedEdit = rbacReleaseName + "-projection-namespaced-edit"
	rbacChartNamespacedView = rbacReleaseName + "-projection-namespaced-view"
	rbacChartClusterAdmin   = rbacReleaseName + "-projection-cluster-admin"
)

// renderChartClusterRoles shells out to `helm template` and parses the
// three RBAC ClusterRoles this test cares about. Brittle-ish (depends on
// helm being on PATH) but the alternative — hardcoding the rules —
// silently drifts when chart authors edit the templates. A missing helm
// binary fails the spec with a clear message rather than a false pass.
func renderChartClusterRoles() (edit, view, clusterAdmin *rbacv1.ClusterRole) {
	chartPath, err := filepath.Abs(filepath.Join("..", "..", "charts", "projection"))
	Expect(err).NotTo(HaveOccurred(), "resolve chart path")

	cmd := exec.Command("helm", "template", rbacReleaseName, chartPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	Expect(cmd.Run()).To(Succeed(), "helm template: %s", stderr.String())

	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(stdout.Bytes()), 4096)
	for {
		var cr rbacv1.ClusterRole
		if err := dec.Decode(&cr); err != nil {
			break
		}
		if cr.Kind != "ClusterRole" {
			// helm renders many kinds; we only care about ClusterRoles.
			// Any non-ClusterRole document still partially populates a
			// ClusterRole struct (with empty Kind), so skip it.
			continue
		}
		switch cr.Name {
		case rbacChartNamespacedEdit:
			c := cr
			edit = &c
		case rbacChartNamespacedView:
			c := cr
			view = &c
		case rbacChartClusterAdmin:
			c := cr
			clusterAdmin = &c
		}
	}

	Expect(edit).NotTo(BeNil(), "chart did not render %q", rbacChartNamespacedEdit)
	Expect(view).NotTo(BeNil(), "chart did not render %q", rbacChartNamespacedView)
	Expect(clusterAdmin).NotTo(BeNil(), "chart did not render %q", rbacChartClusterAdmin)
	return edit, view, clusterAdmin
}

// applyClusterRole upserts a ClusterRole. envtest pre-creates several
// system roles at apiserver bootstrap (notably `admin`, `edit`, `view`,
// and `cluster-admin`), and `edit`/`view`/`admin` come up with zero rules
// because their content is normally filled in by kube-controller-manager
// aggregation — which envtest doesn't run. So a plain Create fails with
// AlreadyExists for those names while leaving the rules empty. We need
// to Get-then-Update on conflict.
func applyClusterRole(cr *rbacv1.ClusterRole) {
	// Strip ResourceVersion etc. that helm template wouldn't have set
	// but a re-applied object might carry. Build a fresh object that
	// Create can accept.
	fresh := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   cr.Name,
			Labels: cr.Labels,
		},
		Rules:           cr.Rules,
		AggregationRule: cr.AggregationRule,
	}
	err := k8sClient.Create(ctx, fresh)
	if err == nil {
		return
	}
	if !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred(), "create ClusterRole %q", cr.Name)
		return
	}
	// AlreadyExists: read the live object's RV, then Update to overwrite
	// rules. Crucial for `edit`/`view`/`admin`, whose envtest stubs are
	// empty.
	live := &rbacv1.ClusterRole{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cr.Name}, live)).
		To(Succeed(), "get live ClusterRole %q", cr.Name)
	live.Labels = cr.Labels
	live.Rules = cr.Rules
	live.AggregationRule = cr.AggregationRule
	Expect(k8sClient.Update(ctx, live)).
		To(Succeed(), "update ClusterRole %q", cr.Name)
}

func applyClusterRoleBinding(name, role string, subjects []rbacv1.Subject) {
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     role,
		},
		Subjects: subjects,
	}
	err := k8sClient.Create(ctx, binding)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred(), "create ClusterRoleBinding %q", name)
	}
}

func applyRoleBinding(namespace, name, clusterRole string, subjects []rbacv1.Subject) {
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterRole,
		},
		Subjects: subjects,
	}
	err := k8sClient.Create(ctx, binding)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred(), "create RoleBinding %s/%s", namespace, name)
	}
}

// userSubject is shorthand for an `rbac.authorization.k8s.io` User subject.
func userSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "User",
		Name:     name,
	}
}

// runSAR issues a SubjectAccessReview against the test apiserver and
// returns the populated status. envtest's apiserver doesn't authenticate
// the requester (it trusts whatever User the SAR carries), so arbitrary
// identifiers like "alice" work.
func runSAR(user, namespace, verb, group, resource string) authorizationv1.SubjectAccessReviewStatus {
	sar := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: user,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      verb,
				Group:     group,
				Resource:  resource,
			},
		},
	}
	Expect(k8sClient.Create(ctx, sar)).To(Succeed(),
		"create SAR for user=%s ns=%s verb=%s %s/%s", user, namespace, verb, group, resource)
	return sar.Status
}

var _ = Describe("Chart RBAC matrix", Ordered, func() {
	const (
		groupProjection      = "projection.sh"
		resourceProjections  = "projections"
		resourceClusterProjs = "clusterprojections"

		tenantA = "rbac-tenant-a"
		tenantB = "rbac-tenant-b"

		userAlice = "alice"
		userBob   = "bob"
		userCarol = "carol"
		userDave  = "dave"
	)

	BeforeAll(func() {
		// Render the three chart ClusterRoles and apply them. The
		// chart stamps aggregation labels on the namespaced-edit/
		// -view roles but envtest has no controller-manager to honor
		// them; the labels are inert here. We compensate below by
		// pre-merging their rules into synthetic edit/view roles.
		chartEdit, chartView, chartClusterAdmin := renderChartClusterRoles()
		applyClusterRole(chartEdit)
		applyClusterRole(chartView)
		applyClusterRole(chartClusterAdmin)

		// Simulate aggregation: overwrite the system `edit` and `view`
		// ClusterRoles with rules sourced from the chart's namespaced-
		// edit/-view templates. envtest's apiserver bootstraps `edit`
		// and `view` as empty stubs (real clusters fill them in via
		// kube-controller-manager aggregation, which envtest doesn't
		// run), so applyClusterRole transparently does Get + Update on
		// AlreadyExists. Same role names so test bindings reflect what
		// tenants would do in production (RoleBinding -> "edit").
		applyClusterRole(&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "edit"},
			Rules:      chartEdit.Rules,
		})
		applyClusterRole(&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "view"},
			Rules:      chartView.Rules,
		})

		// Provision the test namespaces. We use isolated tenant
		// namespaces so matrix rows can't accidentally pass via cross-
		// namespace permissions leaking from other specs in the suite.
		ensureNamespace(tenantA)
		ensureNamespace(tenantB)

		// Per-subject bindings:
		// - alice: edit in tenant-a only (RoleBinding to ClusterRole "edit")
		// - bob:   view in tenant-a only (RoleBinding to ClusterRole "view")
		// - carol: cluster-admin (ClusterRoleBinding to chart cluster-admin role)
		// - dave:  no bindings — control row to confirm unbound denies
		applyRoleBinding(tenantA, "rbac-test-alice-edit", "edit",
			[]rbacv1.Subject{userSubject(userAlice)})
		applyRoleBinding(tenantA, "rbac-test-bob-view", "view",
			[]rbacv1.Subject{userSubject(userBob)})
		applyClusterRoleBinding("rbac-test-carol-cluster-admin", rbacChartClusterAdmin,
			[]rbacv1.Subject{userSubject(userCarol)})
		// Intentionally no binding for dave.
	})

	type matrixRow struct {
		name      string
		user      string
		namespace string // empty string for cluster-scoped requests
		verb      string
		group     string
		resource  string
		allow     bool
	}

	rows := []matrixRow{
		{
			name:      "alice (edit in tenant-a) can create Projections in tenant-a",
			user:      userAlice,
			namespace: tenantA,
			verb:      "create",
			group:     groupProjection,
			resource:  resourceProjections,
			allow:     true,
		},
		{
			name:      "alice (edit in tenant-a) cannot create ClusterProjections",
			user:      userAlice,
			namespace: "",
			verb:      "create",
			group:     groupProjection,
			resource:  resourceClusterProjs,
			allow:     false,
		},
		{
			name:      "alice (edit in tenant-a) cannot create Projections in tenant-b",
			user:      userAlice,
			namespace: tenantB,
			verb:      "create",
			group:     groupProjection,
			resource:  resourceProjections,
			allow:     false,
		},
		{
			name:      "bob (view in tenant-a) can get Projections in tenant-a",
			user:      userBob,
			namespace: tenantA,
			verb:      "get",
			group:     groupProjection,
			resource:  resourceProjections,
			allow:     true,
		},
		{
			name:      "bob (view in tenant-a) cannot create Projections in tenant-a",
			user:      userBob,
			namespace: tenantA,
			verb:      "create",
			group:     groupProjection,
			resource:  resourceProjections,
			allow:     false,
		},
		{
			name:      "carol (cluster-admin) can create ClusterProjections",
			user:      userCarol,
			namespace: "",
			verb:      "create",
			group:     groupProjection,
			resource:  resourceClusterProjs,
			allow:     true,
		},
		{
			name:      "carol (cluster-admin) cannot create Projections in tenant-a",
			user:      userCarol,
			namespace: tenantA,
			verb:      "create",
			group:     groupProjection,
			resource:  resourceProjections,
			allow:     false,
		},
		{
			name:      "dave (unbound) cannot get Projections in tenant-a",
			user:      userDave,
			namespace: tenantA,
			verb:      "get",
			group:     groupProjection,
			resource:  resourceProjections,
			allow:     false,
		},
	}

	for _, row := range rows {
		row := row // capture
		It(row.name, func() {
			status := runSAR(row.user, row.namespace, row.verb, row.group, row.resource)
			if row.allow {
				Expect(status.Allowed).To(BeTrue(),
					"expected ALLOWED but got denied; reason=%q evaluationError=%q",
					status.Reason, status.EvaluationError)
			} else {
				Expect(status.Allowed).To(BeFalse(),
					"expected DENIED but got allowed; reason=%q", status.Reason)
			}
		})
	}

	It("renders all three chart ClusterRoles", func() {
		// Sanity: the chart roles were applied in BeforeAll. If a
		// future refactor breaks the rendering helper, this trips
		// before the access matrix gets blamed.
		for _, name := range []string{
			rbacChartNamespacedEdit,
			rbacChartNamespacedView,
			rbacChartClusterAdmin,
		} {
			cr := &rbacv1.ClusterRole{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, cr)).To(Succeed(),
				"expected ClusterRole %q to exist", name)
			Expect(cr.Rules).NotTo(BeEmpty(), "%q should carry rules", name)
		}
	})
})
