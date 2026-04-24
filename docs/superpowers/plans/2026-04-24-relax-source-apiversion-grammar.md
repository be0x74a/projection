# Relax `source.apiVersion` Grammar — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `Projection.spec.source.apiVersion` to accept an unpinned form (`apps/*`) that resolves to the cluster's preferred served version via RESTMapper. Eliminates surprise destination-GC when CRDs promote versions.

**Architecture:** Three accepted forms — `v1` (core, pinned), `apps/v1` (group, pinned), `apps/*` (group, preferred). `resolveGVR` branches on the `*` sentinel and calls `RESTMapping(GroupKind)` (preferred) vs `RESTMapping(GroupKind, version)` (pinned). The watch indexer (`sourceKey`) drops the version segment so unpinned and pinned Projections both match real `(group, kind, ns, name)` source events. `SourceResolved` condition message enriches with the resolved version when unpinned, giving operators a `kubectl describe`-visible answer to "which version did this pick?"

**Tech Stack:** Go 1.22+, controller-runtime v0.19, Kubebuilder v4, envtest, Ginkgo v2.

---

## Task 0: Branch setup

**Files:** none (git only).

- [ ] **Step 1: Sync local main with remote**

```bash
git fetch origin main:main
```

- [ ] **Step 2: Create feature branch from fresh main**

```bash
git checkout -b feat/relax-source-apiversion-grammar main
```

Expected: clean branch on top of latest `origin/main`.

---

## Task 1: Drop version from `sourceKey` (no-behavior-change refactor)

**Why this first:** The current key includes `apiVersion`, but real source events always carry a fully-qualified GVK. Once we accept `apps/*`, the literal-key join silently breaks. Dropping version is a no-op for the existing pinned-only world (both sides match on the same group prefix today) and a precondition for the new form. Lockstep across three sites — they MUST change together or watches go quiet.

**Files:**
- Modify: `internal/controller/projection_controller.go:163-168` (`sourceKey` helper)
- Modify: `internal/controller/projection_controller.go:480-495` (`mapSource`)
- Modify: `internal/controller/projection_controller.go:999-1013` (`SetupWithManager` field indexer)
- Test: `internal/controller/projection_controller_test.go` (existing shared-watch integration test at line 1304+ exercises the path)

- [ ] **Step 1: Run the existing test suite to record a green baseline**

```bash
make test
```

Expected: PASS. (We need confidence the refactor changes nothing observable.)

- [ ] **Step 2: Update `sourceKey` to take group instead of apiVersion**

Replace lines 163-168:

```go
// sourceKey is the canonical string key identifying a source object across
// both the field indexer and the event-mapping function. The version is
// intentionally omitted: source events always carry a resolved GVK, but a
// Projection may reference its source via an unpinned form (e.g. apps/*).
// Joining on (group, kind, namespace, name) keeps both sides in agreement
// regardless of which served version the apiserver delivered the event for.
func sourceKey(group, kind, namespace, name string) string {
	return group + "/" + kind + "/" + namespace + "/" + name
}
```

- [ ] **Step 3: Update `mapSource` to emit group instead of apiVersion**

In `mapSource` (around line 482), change:

```go
key := sourceKey(gvk.GroupVersion().String(), gvk.Kind, obj.GetNamespace(), obj.GetName())
```

to:

```go
key := sourceKey(gvk.Group, gvk.Kind, obj.GetNamespace(), obj.GetName())
```

- [ ] **Step 4: Update field indexer in `SetupWithManager` to emit group**

In the `IndexField` callback (around line 1005), the call currently is:

```go
return []string{sourceKey(
    p.Spec.Source.APIVersion,
    p.Spec.Source.Kind,
    p.Spec.Source.Namespace,
    p.Spec.Source.Name,
)}
```

Replace with:

```go
gv, err := schema.ParseGroupVersion(p.Spec.Source.APIVersion)
if err != nil {
    // Malformed apiVersion — admission should reject this, but if it
    // ever slips through, indexing under "" rather than panicking
    // keeps the controller alive. Reconcile will surface the error.
    return nil
}
return []string{sourceKey(
    gv.Group,
    p.Spec.Source.Kind,
    p.Spec.Source.Namespace,
    p.Spec.Source.Name,
)}
```

- [ ] **Step 5: Run full test suite — verify still green**

```bash
make test
```

Expected: PASS (no behavior change, just a refactor).

- [ ] **Step 6: Commit**

```bash
git add internal/controller/projection_controller.go
git commit -m "refactor(controller): drop version from sourceKey

Source events always carry a resolved GVK; joining the
field indexer on (group, kind, ns, name) keeps both sides in
agreement regardless of which served version delivered the
event. Precondition for accepting unpinned source.apiVersion
forms in a follow-up commit."
```

---

## Task 2: Refactor `resolveGVR` to return resolved version

**Why:** The condition-message enrichment in Task 5 needs to know which version the RESTMapper picked. Today `resolveGVR` returns only the GVR; the resolved version lives on `mapping.GroupVersionKind.Version` and is dropped on the floor. Make it an explicit return value so callers can pass it through.

**Files:**
- Modify: `internal/controller/projection_controller.go:498-520` (`resolveGVR`)
- Modify: all callers: lines 245, 523 — both ignore the new return for now (Task 5 wires the use site).

- [ ] **Step 1: Update `resolveGVR` signature and body**

Replace lines 498-520:

```go
// resolveGVR maps a SourceRef's apiVersion+kind to a concrete GVR via the
// cached RESTMapper. The second return value is the version the RESTMapper
// picked — equal to gv.Version when the user pinned a version, or the
// preferred served version when unpinned (gv.Version == "*"). Callers
// surface the resolved version in the SourceResolved condition message
// for operator-visibility.
func (r *ProjectionReconciler) resolveGVR(src projectionv1.SourceRef) (schema.GroupVersionResource, string, error) {
	gv, err := schema.ParseGroupVersion(src.APIVersion)
	if err != nil {
		return schema.GroupVersionResource{}, "", fmt.Errorf("parsing apiVersion %q: %w", src.APIVersion, err)
	}
	mapping, err := r.RESTMapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: src.Kind}, gv.Version)
	if err != nil {
		return schema.GroupVersionResource{}, "", fmt.Errorf("resolving %s/%s: %w", src.APIVersion, src.Kind, err)
	}
	if mapping.Scope.Name() != apimeta.RESTScopeNameNamespace {
		return schema.GroupVersionResource{}, "", fmt.Errorf(
			"%s/%s is cluster-scoped; projection only mirrors namespaced resources",
			src.APIVersion, src.Kind)
	}
	return mapping.Resource, mapping.GroupVersionKind.Version, nil
}
```

- [ ] **Step 2: Update both callers to absorb the new return value**

Line 245 (`Reconcile`):

```go
gvr, _, err := r.resolveGVR(proj.Spec.Source)
```

Line 523 (`deleteDestination`):

```go
gvr, _, err := r.resolveGVR(proj.Spec.Source)
```

(The `_` is a deliberate placeholder — Task 5 replaces the first one with a real variable.)

- [ ] **Step 3: Run tests**

```bash
make test
```

Expected: PASS. (Pure signature plumbing; no semantic change.)

- [ ] **Step 4: Commit**

```bash
git add internal/controller/projection_controller.go
git commit -m "refactor(controller): resolveGVR returns resolved version

Plumbs mapping.GroupVersionKind.Version out of resolveGVR so
follow-up work can surface it in the SourceResolved condition
message. Callers currently discard with _; Task 5 wires the
real use site."
```

---

## Task 3: Extend grammar regex + golden + table tests

**Why:** Schema accepts `apps/*` so the apiserver admits new manifests. Add table-driven validation tests at the api/v1 layer (no controller needed, pure regex check). Keep code-level rejection of `*` without a group prefix — the regex is permissive on that for simplicity, and we enforce the constraint in `resolveGVR` (Task 4).

**Files:**
- Modify: `api/v1/projection_types.go:28` (Pattern marker + docstring at line 25)
- Create: `api/v1/projection_types_test.go` (new file, table tests via CRD validation roundtrip — see step 1)
- Modify: `api/v1/testdata/crd.golden.yaml` (regenerated)

- [ ] **Step 1: Check whether api/v1 already has a test file**

```bash
ls /Users/be0x74a/repos/projection/api/v1/
```

Expected output includes `projection_types.go`, `groupversion_info.go`, `zz_generated.deepcopy.go`, `testdata/`. If `projection_types_test.go` exists, modify it; otherwise create per step 4.

- [ ] **Step 2: Update the apiVersion docstring and pattern marker**

In `api/v1/projection_types.go` lines 25-28, replace:

```go
	// APIVersion of the source object, e.g. "v1" or "apps/v1".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^([a-z0-9.-]+/)?v[0-9]+((alpha|beta)[0-9]+)?$`
```

with:

```go
	// APIVersion of the source object. Three forms accepted:
	//   - "v1"      — core group, pinned to v1.
	//   - "apps/v1" — named group, pinned to v1.
	//   - "apps/*"  — named group, RESTMapper-preferred served version.
	// The unpinned form follows the cluster: when a CRD author promotes
	// v1beta1→v1, projection picks up the new preferred version on the
	// next reconcile rather than reporting SourceResolutionFailed.
	// The "*" sentinel is invalid without a group prefix (no "*" form
	// for the core group, which has stable versions); enforced in the
	// reconciler since the regex is permissive for simplicity.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^([a-z0-9.-]+/)?(v[0-9]+((alpha|beta)[0-9]+)?|\*)$`
```

- [ ] **Step 3: Regenerate manifests + golden CRD**

```bash
make manifests
```

Expected: `config/crd/bases/projection.be0x74a.io_projections.yaml` updated with the new pattern. Now propagate to the golden:

```bash
cp config/crd/bases/projection.be0x74a.io_projections.yaml api/v1/testdata/crd.golden.yaml
```

(Adjust the destination filename if the existing golden differs — verify with `diff` and pick the convention the repo already uses.)

- [ ] **Step 4: Add a table test for the pattern**

Look at how the golden test is wired (likely a `TestCRDGolden` in `api/v1/`). If a sibling test file already covers admission patterns via roundtrip, add cases there. Otherwise create `api/v1/projection_apiversion_test.go`:

```go
package v1

import (
	"regexp"
	"testing"
)

// Mirrors the +kubebuilder:validation:Pattern marker on SourceRef.APIVersion.
// Keep in sync — the marker is the source of truth for admission, this is a
// fast unit-level check so a malformed manifest fails before envtest setup.
var apiVersionPattern = regexp.MustCompile(`^([a-z0-9.-]+/)?(v[0-9]+((alpha|beta)[0-9]+)?|\*)$`)

func TestSourceAPIVersionPattern(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// pinned, accepted
		{"v1", true},
		{"v1beta1", true},
		{"v2alpha3", true},
		{"apps/v1", true},
		{"networking.k8s.io/v1", true},
		{"example.com/v1beta1", true},
		// unpinned, accepted
		{"apps/*", true},
		{"example.com/*", true},
		// rejected by regex
		{"", false},
		{"apps/", false},
		{"apps", false},
		{"/v1", false},
		{"APPS/v1", false},
		{"apps//v1", false},
		// regex permits these — code-level rejection lives in resolveGVR (Task 4)
		{"*", true}, // permitted by regex; resolveGVR rejects (no group)
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := apiVersionPattern.MatchString(tc.in)
			if got != tc.want {
				t.Fatalf("MatchString(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 5: Run the table test**

```bash
go test ./api/v1/ -run TestSourceAPIVersionPattern -v
```

Expected: PASS for all rows.

- [ ] **Step 6: Run the full test suite to catch any golden-file drift**

```bash
make test
```

Expected: PASS. If the golden test fails, the fix is to update `api/v1/testdata/crd.golden.yaml` to match the new generated output (step 3 should have done this, but envtest may load from a different location — follow the failure message).

- [ ] **Step 7: Commit**

```bash
git add api/v1/projection_types.go api/v1/projection_apiversion_test.go api/v1/testdata/crd.golden.yaml config/crd/bases/projection.be0x74a.io_projections.yaml
git commit -m "feat(api): accept apps/* as unpinned source.apiVersion form

Regex extension only — controller still rejects \"apps/*\" at
resolveGVR until Task 4 lands the preferred-version branch.
Pattern is permissive on bare \"*\" (no group); resolveGVR
enforces the group requirement in code."
```

---

## Task 4: `resolveGVR` branches on `*` sentinel

**Why:** Make the new form actually work. Branch on `gv.Version == "*"`: pass GroupKind only (RESTMapper picks preferred) vs. GroupKind + version (pinned, today's behavior). Reject bare `*` without a group prefix here.

**Files:**
- Modify: `internal/controller/projection_controller.go:498-520` (the function body modified in Task 2)
- Modify: `internal/controller/projection_controller_test.go` — add unit-level test using a fake RESTMapper.

- [ ] **Step 1: Write the failing test**

In `internal/controller/projection_controller_test.go`, add a new top-level `Describe` block (place after the existing `watchedGvks metric` block around line 1505):

```go
var _ = Describe("resolveGVR", func() {
	var (
		mapper *apimeta.DefaultRESTMapper
		r      *ProjectionReconciler
	)

	BeforeEach(func() {
		mapper = apimeta.NewDefaultRESTMapper([]schema.GroupVersion{
			{Group: "apps", Version: "v1"},
			{Group: "apps", Version: "v1beta2"},
		})
		mapper.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, apimeta.RESTScopeNamespace)
		mapper.Add(schema.GroupVersionKind{Group: "apps", Version: "v1beta2", Kind: "Deployment"}, apimeta.RESTScopeNamespace)
		r = &ProjectionReconciler{RESTMapper: mapper}
	})

	It("returns the pinned version when apiVersion is fully qualified", func() {
		gvr, version, err := r.resolveGVR(projectionv1.SourceRef{
			APIVersion: "apps/v1beta2", Kind: "Deployment",
			Name: "x", Namespace: "y",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(gvr.Version).To(Equal("v1beta2"))
		Expect(version).To(Equal("v1beta2"))
	})

	It("returns the RESTMapper-preferred version when apiVersion uses *", func() {
		gvr, version, err := r.resolveGVR(projectionv1.SourceRef{
			APIVersion: "apps/*", Kind: "Deployment",
			Name: "x", Namespace: "y",
		})
		Expect(err).ToNot(HaveOccurred())
		// DefaultRESTMapper preferred = first GroupVersion in the
		// constructor slice; we passed v1 first.
		Expect(gvr.Version).To(Equal("v1"))
		Expect(version).To(Equal("v1"))
	})

	It("rejects bare * without a group prefix", func() {
		_, _, err := r.resolveGVR(projectionv1.SourceRef{
			APIVersion: "*", Kind: "Deployment",
			Name: "x", Namespace: "y",
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("group is required"))
	})
})
```

- [ ] **Step 2: Run the test — verify it fails**

```bash
go test ./internal/controller/ -run "resolveGVR" -v
```

Expected: FAIL on the `apps/*` case (current code passes `*` as a literal version to RESTMapper, which returns NoMatchKind), AND the bare-`*` case (current code accepts and forwards to RESTMapper).

- [ ] **Step 3: Implement the branch**

Replace the body of `resolveGVR` (the function reshaped in Task 2):

```go
func (r *ProjectionReconciler) resolveGVR(src projectionv1.SourceRef) (schema.GroupVersionResource, string, error) {
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
		mapping, err = r.RESTMapper.RESTMapping(gk)
	default:
		// Pinned to a specific version (today's behavior).
		mapping, err = r.RESTMapper.RESTMapping(gk, gv.Version)
	}
	if err != nil {
		return schema.GroupVersionResource{}, "", fmt.Errorf("resolving %s/%s: %w", src.APIVersion, src.Kind, err)
	}
	if mapping.Scope.Name() != apimeta.RESTScopeNameNamespace {
		return schema.GroupVersionResource{}, "", fmt.Errorf(
			"%s/%s is cluster-scoped; projection only mirrors namespaced resources",
			src.APIVersion, src.Kind)
	}
	return mapping.Resource, mapping.GroupVersionKind.Version, nil
}
```

- [ ] **Step 4: Run the test — verify it passes**

```bash
go test ./internal/controller/ -run "resolveGVR" -v
```

Expected: PASS for all three `It` blocks.

- [ ] **Step 5: Run full suite to confirm no regression**

```bash
make test
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/controller/projection_controller.go internal/controller/projection_controller_test.go
git commit -m "feat(controller): resolveGVR branches on * sentinel

apps/* delegates to RESTMapper.RESTMapping(GroupKind) which
returns the preferred served version. apps/v1 (pinned) keeps
today's RESTMapping(GroupKind, version) path. Bare * without
a group prefix is rejected with a clear error."
```

---

## Task 5: Surface resolved version in SourceResolved condition message

**Why:** When a Projection uses `apps/*`, operators need to be able to answer "which version is this currently on?" without operator log access. Embed the resolved version in the `SourceResolved` condition message so `kubectl describe projection` shows it. Pinned form keeps today's empty message (no behavior change for existing users).

**Files:**
- Modify: `internal/controller/projection_controller.go:245` (Reconcile — capture resolved version)
- Modify: `internal/controller/projection_controller.go:837-851` (`failDestination`)
- Modify: `internal/controller/projection_controller.go:854-862` (`markAllReady`)
- Modify: `internal/controller/projection_controller.go:355-358` (rollup call site after fan-out)
- Modify: `internal/controller/projection_controller_test.go` — extend an existing happy-path integration test to assert message content for unpinned, and assert empty for pinned.

- [ ] **Step 1: Add a helper that formats the resolved-version message**

In `internal/controller/projection_controller.go`, near `sourceKey` (around line 168), add:

```go
// resolvedVersionMessage produces the human-readable SourceResolved
// condition message when a Projection used the unpinned form (apps/*) and
// the RESTMapper picked a concrete version. Returns "" for pinned sources
// to preserve today's empty-message behavior.
func resolvedVersionMessage(src projectionv1.SourceRef, resolvedVersion string) string {
	gv, err := schema.ParseGroupVersion(src.APIVersion)
	if err != nil || gv.Version != "*" {
		return ""
	}
	return fmt.Sprintf("resolved %s/%s to preferred version %s",
		gv.Group, src.Kind, resolvedVersion)
}
```

- [ ] **Step 2: Capture the resolved version in Reconcile**

Line 245 currently reads:

```go
gvr, _, err := r.resolveGVR(proj.Spec.Source)
```

Replace with:

```go
gvr, resolvedVersion, err := r.resolveGVR(proj.Spec.Source)
```

- [ ] **Step 3: Thread `resolvedVersion` into `markAllReady` and `failDestination`**

Update `markAllReady` signature (line 854):

```go
func (r *ProjectionReconciler) markAllReady(ctx context.Context, proj *projectionv1.Projection, resolvedVersion string) error {
	msg := resolvedVersionMessage(proj.Spec.Source, resolvedVersion)
	setCondition(proj, conditionSourceResolved, metav1.ConditionTrue, "Resolved", msg)
	setCondition(proj, conditionDestinationWritten, metav1.ConditionTrue, "Projected", "")
	setCondition(proj, conditionReady, metav1.ConditionTrue, "Projected", "")
	if err := r.Status().Update(ctx, proj); err != nil {
		return err
	}
	// preserve any existing trailing logic
}
```

(Read the current body and preserve the metric/return tail — only the first three `setCondition` calls change.)

Update `failDestination` signature (line 837):

```go
func (r *ProjectionReconciler) failDestination(ctx context.Context, proj *projectionv1.Projection, resolvedVersion, reason, msg string) (ctrl.Result, error) {
	srMsg := resolvedVersionMessage(proj.Spec.Source, resolvedVersion)
	setCondition(proj, conditionSourceResolved, metav1.ConditionTrue, "Resolved", srMsg)
	setCondition(proj, conditionDestinationWritten, metav1.ConditionFalse, reason, msg)
	setCondition(proj, conditionReady, metav1.ConditionFalse, reason, msg)
	// preserve switch + Status().Update tail unchanged
}
```

- [ ] **Step 4: Update all call sites**

`failDestination` callers (grep for `failDestination(`):

- Line 242 (validation failure — happens before resolveGVR runs, no version available): pass `""`.
- Line 285 (namespace resolution failed — after resolveGVR): pass `resolvedVersion`.
- Line 355 (rollup after fan-out — after resolveGVR): pass `resolvedVersion`.

`markAllReady` caller:

- Line 358: pass `resolvedVersion`.

Run `grep -n "failDestination\|markAllReady" internal/controller/projection_controller.go` and update each call.

- [ ] **Step 5: Add/extend integration assertions**

In `internal/controller/projection_controller_test.go`, find the existing happy-path `It` (around line 207, "projects the source to the destination with overlay applied"). After the existing assertions on the destination, add:

```go
By("the SourceResolved condition message is empty for pinned apiVersion forms (regression)")
projAfter := &projectionv1.Projection{}
Expect(k8sClient.Get(ctx, projKey, projAfter)).To(Succeed())
sr := apimeta.FindStatusCondition(projAfter.Status.Conditions, "SourceResolved")
Expect(sr).ToNot(BeNil())
Expect(sr.Status).To(Equal(metav1.ConditionTrue))
Expect(sr.Message).To(Equal(""), "pinned form must keep empty message — operators rely on this in dashboards")
```

(Match the variable names used in that test — `projKey` may be named differently; substitute.)

Then add a new `It` block in the same `Context("Create path", ...)` block:

```go
It("populates SourceResolved.message with the resolved version when apiVersion is unpinned", func() {
	srcNS := uniqueNS("unpinned-src")
	dstNS := uniqueNS("unpinned-dst")
	createNamespace(srcNS)
	createNamespace(dstNS)

	src := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "src-cm",
			Namespace:   srcNS,
			Annotations: map[string]string{projectableAnnotation: "true"},
		},
		Data: map[string]string{"k": "v"},
	}
	Expect(k8sClient.Create(ctx, src)).To(Succeed())

	// Use "/*" — for the core group there is no unpinned form, so fall
	// back to a CRD-style example only if the test environment registers
	// one. For ConfigMap, use a custom group? — NO: ConfigMap is core.
	// This test must be skipped or rewritten if envtest doesn't have a
	// non-core resource available. Use the apps/Deployment that is part
	// of the standard envtest scheme.
	dep := &appsv1.Deployment{
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
	Expect(k8sClient.Create(ctx, dep)).To(Succeed())

	proj := &projectionv1.Projection{
		ObjectMeta: metav1.ObjectMeta{Name: "unpinned-proj", Namespace: srcNS},
		Spec: projectionv1.ProjectionSpec{
			Source: projectionv1.SourceRef{
				APIVersion: "apps/*", Kind: "Deployment",
				Name: "src-dep", Namespace: srcNS,
			},
			Destination: projectionv1.DestinationRef{Namespace: dstNS},
		},
	}
	Expect(k8sClient.Create(ctx, proj)).To(Succeed())

	Eventually(func() string {
		got := &projectionv1.Projection{}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(proj), got); err != nil {
			return ""
		}
		c := apimeta.FindStatusCondition(got.Status.Conditions, "SourceResolved")
		if c == nil {
			return ""
		}
		return c.Message
	}, "10s", "200ms").Should(Equal("resolved apps/Deployment to preferred version v1"))
})
```

(Add `appsv1 "k8s.io/api/apps/v1"` to the imports if not already there. Verify Deployment is in the envtest scheme by grepping `suite_test.go` for `appsv1`.)

- [ ] **Step 6: Run the new tests**

```bash
go test ./internal/controller/ -v -ginkgo.focus="SourceResolved|unpinned"
```

Expected: PASS. If Deployment isn't in the envtest scheme, the test will fail at `Create(dep)` — fix by adding `appsv1.AddToScheme` in `suite_test.go` (small, sibling-pattern change).

- [ ] **Step 7: Run full suite**

```bash
make test
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/controller/projection_controller.go internal/controller/projection_controller_test.go internal/controller/suite_test.go
git commit -m "feat(controller): SourceResolved.message reports resolved version

When source.apiVersion uses the unpinned form (apps/*), the
SourceResolved condition message reports which version the
RESTMapper picked: 'resolved apps/Deployment to preferred
version v1'. Pinned forms keep today's empty message.
Operators can answer 'which version is this on?' via
kubectl describe without operator log access."
```

---

## Task 6: Shared-watch coverage across pinned + unpinned forms

**Why:** The `sourceKey` refactor in Task 1 enables sharing watches. Add an integration test that two Projections — one `apps/v1`, one `apps/*` — referencing the same source register exactly one watch and both reconcile from the same event.

**Files:**
- Modify: `internal/controller/projection_controller_test.go` — extend the existing "Shared source watch (integration with manager)" Describe block (line 1304+).

- [ ] **Step 1: Add a new `It` block**

After the existing `It("two Projections sharing a source GVK ...")` (line 1358), add:

```go
It("a pinned and an unpinned Projection on the same source share one watch entry", func() {
	srcNS := uniqueNS("mixedwatch-src")
	dstNS1 := uniqueNS("mixedwatch-dst1")
	dstNS2 := uniqueNS("mixedwatch-dst2")
	createNamespace(srcNS)
	createNamespace(dstNS1)
	createNamespace(dstNS2)

	srcName := "shared-dep"
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
		name       string
		apiVersion string
		destNS     string
	}{
		{"pinned-proj", "apps/v1", dstNS1},
		{"unpinned-proj", "apps/*", dstNS2},
	} {
		proj := &projectionv1.Projection{
			ObjectMeta: metav1.ObjectMeta{Name: spec.name, Namespace: srcNS},
			Spec: projectionv1.ProjectionSpec{
				Source: projectionv1.SourceRef{
					APIVersion: spec.apiVersion, Kind: "Deployment",
					Name: srcName, Namespace: srcNS,
				},
				Destination: projectionv1.DestinationRef{Namespace: spec.destNS},
			},
		}
		Expect(k8sClient.Create(ctx, proj)).To(Succeed())
		DeferCleanup(deleteProjection, client.ObjectKeyFromObject(proj))
	}

	By("both destinations are written")
	for _, ns := range []string{dstNS1, dstNS2} {
		Eventually(func() error {
			d := &appsv1.Deployment{}
			return k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: srcName}, d)
		}, "10s", "200ms").Should(Succeed())
	}

	By("the watched-GVK map has exactly one entry for apps/v1/Deployment")
	sharedR.watchedMu.Lock()
	entries := len(sharedR.watched)
	_, hasDep := sharedR.watched[schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}]
	sharedR.watchedMu.Unlock()
	Expect(entries).To(Equal(1), "pinned + unpinned should share a single watch; got %d entries", entries)
	Expect(hasDep).To(BeTrue(), "apps/v1/Deployment GVK not in watched map")

	By("editing the source — both destinations reflect the change via the shared watch")
	Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: srcNS, Name: srcName}, src)).To(Succeed())
	if src.Spec.Template.Annotations == nil {
		src.Spec.Template.Annotations = map[string]string{}
	}
	src.Spec.Template.Annotations["bump"] = "1"
	Expect(k8sClient.Update(ctx, src)).To(Succeed())

	for _, ns := range []string{dstNS1, dstNS2} {
		Eventually(func() string {
			d := &appsv1.Deployment{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: srcName}, d); err != nil {
				return ""
			}
			return d.Spec.Template.Annotations["bump"]
		}, "10s", "200ms").Should(Equal("1"))
	}
})
```

**Important precondition:** the `sharedR` reconciler must use a RESTMapper that knows about `apps/v1/Deployment`. The existing `Ordered` block at line 1304 already wires one for `v1/ConfigMap`; verify by reading the `BeforeAll` setup. If `apps/v1` isn't registered, the `apps/*` resolution will fail — extend the BeforeAll setup as needed (sibling pattern).

- [ ] **Step 2: Run the new test**

```bash
go test ./internal/controller/ -v -ginkgo.focus="pinned and an unpinned"
```

Expected: PASS.

- [ ] **Step 3: Run full suite**

```bash
make test
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/projection_controller_test.go
git commit -m "test(controller): pinned + unpinned share a single watch entry

Extends the shared-watch integration test to assert that the
post-Task-1 sourceKey refactor + post-Task-4 resolveGVR
branching keeps both forms keyed on the same (group, kind,
ns, name) — single watch, both Projections reconcile from
the same event."
```

---

## Task 7: Multi-served-version test (best-effort)

**Why:** Strongest possible coverage of the preferred-version contract — wire a custom CRD with two served versions (no conversion webhook, identical schemas) and assert `apps/*`-style resolution picks the storage version. May not be feasible with envtest's RESTMapper caching; if blocked, leave a TODO comment and surface the limitation in the PR body.

**Files:**
- Create: `internal/controller/preferred_version_test.go` (new file, isolates the heavyweight CRD setup from the main suite)

- [ ] **Step 1: Investigate envtest multi-version-CRD support**

Read controller-runtime's envtest docs and existing usage in this repo. Specifically:

- `sigs.k8s.io/controller-runtime/pkg/envtest` — `Environment.CRDs` field accepts `*apiextensionsv1.CustomResourceDefinition` directly.
- Verify whether the manager's RESTMapper picks up a CRD added at runtime (not pre-loaded in `BeforeSuite`). If it caches, you'll need to call `meta.MaybeResetRESTMapper(mapper)` or restart the manager — likely too invasive.

If runtime-added CRDs don't work cleanly, fall back to defining the CRD in `BeforeSuite` alongside the existing Projection CRD load.

- [ ] **Step 2: If feasible, write the test**

Sketch:

```go
var _ = Describe("Preferred-version resolution against a multi-version CRD", func() {
	It("picks the storage version when source.apiVersion is unpinned", func() {
		// Define a Widget CRD with v1alpha1 + v1, both served, conversion=None,
		// storage=v1. (Use apiextensionsv1 client to install it.)
		// Create a Widget instance via v1.
		// Create a Projection: source.apiVersion = "example.com/*", kind=Widget.
		// Eventually(): destination Widget exists in dst namespace.
		// Assert the SourceResolved.message contains "v1".
	})
})
```

Full implementation requires reading `apiextensionsv1` CRD shape and is omitted here — write it from the apimachinery docs.

- [ ] **Step 3: If blocked, leave a TODO and exit cleanly**

If RESTMapper cache invalidation defeats the test, replace the file content with a single-line comment:

```go
// TODO(v1.0): exercise preferred-version resolution against a custom
// multi-version CRD. Blocked on envtest RESTMapper cache behavior —
// see PR body for details. Track in v1.0 hardening pass.
package controller
```

**Important:** spend at most 60 minutes on this task. If not converging, fall back to the TODO and commit.

- [ ] **Step 4: Run the test (or confirm clean compile if TODO-only)**

```bash
go test ./internal/controller/ -v -ginkgo.focus="Preferred-version"
```

Expected: PASS, OR file is comment-only and `make test` is still green.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/preferred_version_test.go
git commit -m "test(controller): preferred-version resolution against multi-version CRD"
```

(Adjust commit message if the file is a TODO-only stub: `test(controller): note multi-version-CRD coverage gap (TODO v1.0)`.)

---

## Task 8: Docs updates

**Why:** Explicit ticket deliverable. The unpinned form should be visible as the default-recommended approach across the docs site, not buried in a CHANGELOG.

**Files:**
- Modify: `docs/concepts.md` — add "Pinned vs. preferred version" subsection.
- Modify: `docs/crd-reference.md` — document the three forms.
- Modify: `docs/getting-started.md` — switch the CRD-source illustration to unpinned.
- Modify: `docs/index.md` — keep `v1` for the core ConfigMap demo (no preferred form for core).
- Modify: `config/samples/` — at least one sample uses `apps/*`.

- [ ] **Step 1: Add the concepts.md subsection**

Read `docs/concepts.md`. Find a natural insertion point (likely after the existing source/destination overview). Add:

```markdown
## Pinned vs. preferred version

`source.apiVersion` accepts three forms:

| Form | Semantics |
| --- | --- |
| `v1` | Core group, pinned to v1. |
| `apps/v1` | Named group, pinned to v1. |
| `apps/*` | Named group, RESTMapper-preferred served version. |

**Pinned** is an explicit stability anchor: useful when you're mid-migration
and want to lock the projection to a specific version while you validate
behavior, or when you intentionally need to fall behind a CRD upgrade.

**Preferred** (`apps/*`) is the default recommendation. It follows the
cluster: when a CRD author promotes `v1beta1` → `v1` and stops serving
`v1beta1`, projection picks up the new preferred version on the next
reconcile rather than failing with `SourceResolutionFailed` and
garbage-collecting your destinations.

The resolved version is reported in the `SourceResolved` condition message
(`kubectl describe projection`), so you can always answer "which version
is this currently on?" without operator log access.

The core group does not have an unpinned form — its versions are stable.
```

- [ ] **Step 2: Add a grammar section to crd-reference.md**

Read `docs/crd-reference.md`. Find the section that describes `source.apiVersion`. Replace the description with the same three-form table from step 1, plus an "Examples" subsection:

```markdown
**Examples:**

```yaml
# Mirror a ConfigMap (core group, pinned)
source:
  apiVersion: v1
  kind: ConfigMap

# Mirror a Deployment (named group, pinned)
source:
  apiVersion: apps/v1
  kind: Deployment

# Mirror a custom resource, following the cluster's preferred version
source:
  apiVersion: example.com/*
  kind: Widget
```

```

- [ ] **Step 3: Update the getting-started CRD illustration**

In `docs/getting-started.md`, find any non-core-group example (e.g. `apps/v1` or a CRD reference). If one exists in a "real-world" walkthrough section, switch it to the unpinned form (`apps/*`). If only core-group ConfigMap/Secret examples exist, **add** a brief CRD example using the unpinned form to demonstrate the pattern. Do not overwrite the core-group examples — those stay pinned.

- [ ] **Step 4: Audit `config/samples/`**

```bash
ls config/samples/
```

Currently: `projection_v1_projection.yaml` (likely a ConfigMap source) + `projection_v1_projection_selector.yaml` (selector demo). Read both. If either targets a non-core group, switch to the unpinned form. If both are core-group only, **add** a third sample, `projection_v1_projection_unpinned.yaml`, demonstrating an `apps/*` Deployment mirror:

```yaml
apiVersion: projection.be0x74a.io/v1
kind: Projection
metadata:
  name: deployment-mirror-unpinned
  namespace: default
spec:
  source:
    apiVersion: apps/*
    kind: Deployment
    name: my-deployment
    namespace: source-ns
  destination:
    namespace: dest-ns
```

If you add a new sample, also add it to `config/samples/kustomization.yaml`:

```yaml
resources:
- projection_v1_projection.yaml
- projection_v1_projection_selector.yaml
- projection_v1_projection_unpinned.yaml
```

- [ ] **Step 5: Build the docs site to catch broken links / formatting**

```bash
# If the project uses mkdocs:
mkdocs build --strict 2>&1 | tail -20
```

Expected: clean build, no warnings. If the project uses a different docs builder, run that command.

- [ ] **Step 6: Commit**

```bash
git add docs/concepts.md docs/crd-reference.md docs/getting-started.md config/samples/
git commit -m "docs: document pinned vs. preferred source.apiVersion forms

Adds 'Pinned vs. preferred version' subsection to concepts.md
explaining the trade-off and recommending the unpinned form
as default for CRD sources. crd-reference.md documents all
three accepted forms with examples. Getting-started + a new
sample demonstrate the unpinned form on a Deployment source."
```

---

## Task 9: Final regen + lint sweep

**Why:** Catch any drift between source-of-truth (markers, code) and generated artifacts (CRD YAML, deepcopy, api-reference docs) before the PR. CI will run drift checks.

**Files:** all generated files; no hand edits.

- [ ] **Step 1: Run the full generation pipeline**

```bash
make manifests generate docs-ref
```

Expected: zero or only-whitespace diffs vs. what's already committed. If anything substantive changes, the prior tasks left work undone — investigate and fix at the source.

- [ ] **Step 2: Run lint**

```bash
make lint
```

Expected: PASS. Fix any issues (typically formatting or unused imports).

- [ ] **Step 3: Run the full test suite one more time**

```bash
make test
```

Expected: PASS.

- [ ] **Step 4: Stage any generated drift and commit**

```bash
git status
# Inspect, then:
git add -A
git diff --cached  # eyeball before committing
git commit -m "chore: regenerate manifests, deepcopy, api-reference"
```

(Skip the commit if `git status` is clean.)

---

## Task 10: Open the PR

**Why:** Land the work. Per memory, no AI co-author trailers, no direct push to main.

- [ ] **Step 1: Push the branch**

```bash
git push -u origin feat/relax-source-apiversion-grammar
```

- [ ] **Step 2: Open the PR**

```bash
gh pr create --title "feat(api): relax source.apiVersion — accept apps/* as preferred-version form" --body "$(cat <<'EOF'
## Summary

- `source.apiVersion` now accepts `apps/*` (named group, RESTMapper-preferred served version) in addition to today's pinned forms (`v1`, `apps/v1`).
- Eliminates surprise destination-GC when a CRD author promotes `v1beta1` → `v1` and stops serving the old version — the controller follows the cluster instead of failing `SourceResolutionFailed`.
- `SourceResolved` condition message now reports the resolved version when unpinned (`resolved apps/Deployment to preferred version v1`); pinned forms keep today's empty message.
- `sourceKey` now joins on `(group, kind, ns, name)` so pinned and unpinned Projections referencing the same source share a single watch and both wake on the same event.

## Why now (v0.2.0)

Zero users / zero clones today — free to extend the grammar without breaking anyone. Pre-v1.0 stability promise doesn't bind yet. Every existing pinned manifest continues to validate and behave identically.

## Three accepted forms

| Form | Semantics |
| --- | --- |
| `v1` | Core, pinned (unchanged). |
| `apps/v1` | Group, pinned (unchanged). |
| `apps/*` | Group, **preferred served version** (new). |

Bare `*` without a group prefix is rejected (no unpinned form for the core group — its versions are stable).

## Test plan

- [ ] `make test` — full suite green, including new resolveGVR unit tests + shared-watch integration test exercising pinned+unpinned coexistence.
- [ ] `make lint` — clean.
- [ ] `make test-e2e` — green on a fresh Kind cluster.
- [ ] Manual smoke: install a multi-version CRD on a kind cluster, create a Projection with `apiVersion: example.com/*`, confirm SourceResolved message reports the storage version. Promote storage version, observe the Projection follow.

## Notes

- The multi-served-version envtest case is [TODO/landed depending on Task 7 outcome — fill in before merging].
EOF
)"
```

- [ ] **Step 3: Move the project board item to In Review**

```bash
gh project item-list 3 --owner be0x74a --format json --limit 50 \
  | jq -r '.items[] | select(.content.title | contains("relax source.apiVersion")) | .id'
# then use the returned id to update the status; check `gh project item-edit` syntax
```

(The exact command may need looking up — keep it manual if uncertain.)

---

## Self-review checklist

- [ ] Spec coverage: regex change, controller branch, sourceKey refactor, watch sharing, condition message, docs, samples, multi-version test (best-effort) — all mapped to tasks.
- [ ] No placeholders: every step has concrete code/commands or an explicit fall-back path (Task 7).
- [ ] Type consistency: `resolveGVR` returns `(GVR, string, error)` everywhere; `markAllReady` and `failDestination` take a `resolvedVersion string` first parameter consistently after Task 5.
- [ ] User memory respected: no AI co-author trailers; no direct main pushes; sync main first; subagent-driven execution next.
