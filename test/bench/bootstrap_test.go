package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// asMap chases a path of keys through an unstructured.Object map. Returns
// (sub-map, true) if every key resolves to a map, (nil, false) otherwise.
func asMap(t *testing.T, u *unstructured.Unstructured, path ...string) (map[string]interface{}, bool) {
	t.Helper()
	cur := u.Object
	for _, k := range path {
		v, ok := cur[k]
		if !ok {
			return nil, false
		}
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur = m
	}
	return cur, true
}

func TestBuildNamespacedProjection_V03Shape(t *testing.T) {
	u := buildNamespacedProjection("dst-ns", "proj-1", "src-ns", "src-1", "BenchObject0")

	// Top-level metadata.
	if got := u.GetAPIVersion(); got != "projection.sh/v1" {
		t.Errorf("apiVersion: got %q, want %q", got, "projection.sh/v1")
	}
	if got := u.GetKind(); got != "Projection" {
		t.Errorf("kind: got %q, want %q", got, "Projection")
	}
	if got := u.GetNamespace(); got != "dst-ns" {
		t.Errorf("metadata.namespace: got %q, want %q", got, "dst-ns")
	}
	if got := u.GetName(); got != "proj-1" {
		t.Errorf("metadata.name: got %q, want %q", got, "proj-1")
	}

	// spec.source uses v0.3 split (group + version + kind), NOT apiVersion.
	src, ok := asMap(t, u, "spec", "source")
	if !ok {
		t.Fatalf("spec.source missing or not a map: %+v", u.Object)
	}
	if _, has := src["apiVersion"]; has {
		t.Errorf("spec.source must not carry legacy apiVersion field: %+v", src)
	}
	if got := src["group"]; got != "bench.projection.sh" {
		t.Errorf("spec.source.group: got %v, want %q", got, "bench.projection.sh")
	}
	if got := src["version"]; got != "v1" {
		t.Errorf("spec.source.version: got %v, want %q", got, "v1")
	}
	if got := src["kind"]; got != "BenchObject0" {
		t.Errorf("spec.source.kind: got %v, want %q", got, "BenchObject0")
	}
	if got := src["namespace"]; got != "src-ns" {
		t.Errorf("spec.source.namespace: got %v, want %q", got, "src-ns")
	}
	if got := src["name"]; got != "src-1" {
		t.Errorf("spec.source.name: got %v, want %q", got, "src-1")
	}

	// spec.destination must NOT carry namespace or namespaceSelector.
	dst, ok := asMap(t, u, "spec", "destination")
	if !ok {
		t.Fatalf("spec.destination missing or not a map: %+v", u.Object)
	}
	if _, has := dst["namespace"]; has {
		t.Errorf("namespaced Projection must not set destination.namespace (v0.3 invariant): %+v", dst)
	}
	if _, has := dst["namespaceSelector"]; has {
		t.Errorf("namespaced Projection must not set destination.namespaceSelector (v0.3 moved this to ClusterProjection): %+v", dst)
	}
	if got := dst["name"]; got != "src-1" {
		t.Errorf("spec.destination.name: got %v, want %q", got, "src-1")
	}
	// Tightness: catch the case where a future builder change accidentally
	// leaks an extra destination field (e.g. an Overlay field misrouted
	// here). The only acceptable destination key on a namespaced Projection
	// is `name`.
	if got := len(dst); got != 1 {
		t.Errorf("spec.destination should have exactly one field (name), got %d: %+v", got, dst)
	}
}

func TestBuildClusterProjectionSelector_V03Shape(t *testing.T) {
	u := buildClusterProjectionSelector("cp-sel-0", "src-ns", "src-0", "BenchObject0", "match", "yes")

	if got := u.GetKind(); got != "ClusterProjection" {
		t.Errorf("kind: got %q, want %q", got, "ClusterProjection")
	}
	// Cluster-scoped: namespace must be empty.
	if got := u.GetNamespace(); got != "" {
		t.Errorf("ClusterProjection must have no metadata.namespace: got %q", got)
	}

	// SourceRef shape mirrors namespaced case.
	src, ok := asMap(t, u, "spec", "source")
	if !ok {
		t.Fatalf("spec.source missing")
	}
	if _, has := src["apiVersion"]; has {
		t.Errorf("source must not carry legacy apiVersion: %+v", src)
	}
	if src["group"] != "bench.projection.sh" || src["version"] != "v1" {
		t.Errorf("source group/version: %+v", src)
	}

	// destination has namespaceSelector but NOT namespaces.
	dst, ok := asMap(t, u, "spec", "destination")
	if !ok {
		t.Fatalf("spec.destination missing")
	}
	if _, has := dst["namespaces"]; has {
		t.Errorf("CP-selector destination must not include explicit namespaces list: %+v", dst)
	}
	sel, ok := asMap(t, u, "spec", "destination", "namespaceSelector")
	if !ok {
		t.Fatalf("destination.namespaceSelector missing or not a map: %+v", dst)
	}
	ml, ok := sel["matchLabels"].(map[string]interface{})
	if !ok {
		t.Fatalf("destination.namespaceSelector.matchLabels not a map: %+v", sel)
	}
	if ml["match"] != "yes" {
		t.Errorf("matchLabels: got %+v, want match=yes", ml)
	}
}

func TestBuildClusterProjectionList_V03Shape(t *testing.T) {
	nss := []string{"a", "b", "c"}
	u := buildClusterProjectionList("cp-list-0", "src-ns", "src-0", "BenchObject0", nss)

	if got := u.GetKind(); got != "ClusterProjection" {
		t.Errorf("kind: got %q, want %q", got, "ClusterProjection")
	}
	if got := u.GetNamespace(); got != "" {
		t.Errorf("ClusterProjection must have no metadata.namespace: got %q", got)
	}

	dst, ok := asMap(t, u, "spec", "destination")
	if !ok {
		t.Fatalf("spec.destination missing")
	}
	// destination has namespaces but NOT namespaceSelector.
	if _, has := dst["namespaceSelector"]; has {
		t.Errorf("CP-list destination must not include namespaceSelector: %+v", dst)
	}
	rawList, ok := dst["namespaces"].([]interface{})
	if !ok {
		t.Fatalf("destination.namespaces not a []interface{}: %+v", dst)
	}
	if len(rawList) != len(nss) {
		t.Fatalf("destination.namespaces length: got %d, want %d", len(rawList), len(nss))
	}
	for i, n := range nss {
		if got, _ := rawList[i].(string); got != n {
			t.Errorf("destination.namespaces[%d]: got %v, want %q", i, rawList[i], n)
		}
	}
}

// TestBuildSourceRef_NoApiVersionField guards against accidental regression
// to the legacy v0.1/v0.2 shape, which packed apiVersion+kind into the
// SourceRef. The v0.3 split is structural: group+version are independent
// fields with their own CEL rules.
func TestBuildSourceRef_NoApiVersionField(t *testing.T) {
	src := buildSourceRef("ns", "obj", "BenchObject7")
	if _, has := src["apiVersion"]; has {
		t.Errorf("buildSourceRef must not produce apiVersion field: %+v", src)
	}
	for _, want := range []string{"group", "version", "kind", "namespace", "name"} {
		if _, has := src[want]; !has {
			t.Errorf("buildSourceRef missing field %q: %+v", want, src)
		}
	}
}
