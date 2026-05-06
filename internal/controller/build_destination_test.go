/*
Copyright 2026 The projection Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// newSourceCM constructs a minimal ConfigMap-shaped unstructured object for
// tests. Callers can poke at the returned object to add fields they care
// about.
func newSourceCM(name, namespace string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("v1")
	u.SetKind("ConfigMap")
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

// newProj builds a Projection with the given metadata and spec bits.
func newProj(projNs, projName string, src projectionv1.SourceRef, dst projectionv1.ProjectionDestination, overlay projectionv1.Overlay) *projectionv1.Projection {
	return &projectionv1.Projection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projName,
			Namespace: projNs,
			// Deterministic UID so buildDestination's ownedByUIDLabel
			// comparisons are stable across test runs. In envtest and real
			// clusters the apiserver allocates this.
			UID: "00000000-0000-0000-0000-000000000001",
		},
		Spec: projectionv1.ProjectionSpec{
			Source:      src,
			Destination: dst,
			Overlay:     overlay,
		},
	}
}

func TestBuildDestination(t *testing.T) {
	type checkFn func(t *testing.T, dst *unstructured.Unstructured)

	baseSrc := projectionv1.SourceRef{
		Group:     "",
		Version:   "v1",
		Kind:      "ConfigMap",
		Name:      "src-cm",
		Namespace: "src-ns",
	}

	tests := []struct {
		name    string
		source  func() *unstructured.Unstructured
		proj    *projectionv1.Projection
		asserts checkFn
	}{
		{
			name: "strips server-owned metadata",
			source: func() *unstructured.Unstructured {
				u := newSourceCM("src-cm", "src-ns")
				u.SetResourceVersion("12345")
				u.SetUID("abcd-efgh")
				u.SetGeneration(7)
				now := metav1.Now()
				u.SetCreationTimestamp(now)
				u.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl"}})
				u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "owner", UID: "xxxx"}})
				u.SetFinalizers([]string{"example.com/finalizer"})
				return u
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				if got := dst.GetResourceVersion(); got != "" {
					t.Errorf("resourceVersion = %q, want empty", got)
				}
				if got := dst.GetUID(); got != "" {
					t.Errorf("uid = %q, want empty", got)
				}
				if got := dst.GetGeneration(); got != 0 {
					t.Errorf("generation = %d, want 0", got)
				}
				if got := dst.GetManagedFields(); len(got) != 0 {
					t.Errorf("managedFields = %v, want empty", got)
				}
				if got := dst.GetOwnerReferences(); len(got) != 0 {
					t.Errorf("ownerReferences = %v, want empty", got)
				}
				if got := dst.GetFinalizers(); len(got) != 0 {
					t.Errorf("finalizers = %v, want empty", got)
				}
				// creationTimestamp is a special case: SetCreationTimestamp
				// leaves a field in metadata, but buildDestination should
				// have removed it from the underlying map.
				metadata, _ := dst.Object["metadata"].(map[string]interface{})
				if _, exists := metadata["creationTimestamp"]; exists {
					t.Errorf("creationTimestamp still present in metadata: %v", metadata["creationTimestamp"])
				}
				if _, exists := metadata["resourceVersion"]; exists {
					t.Errorf("resourceVersion still present in metadata")
				}
				if _, exists := metadata["uid"]; exists {
					t.Errorf("uid still present in metadata")
				}
			},
		},
		{
			name: "drops .status",
			source: func() *unstructured.Unstructured {
				u := newSourceCM("src-cm", "src-ns")
				u.Object["status"] = map[string]interface{}{
					"phase": "Running",
				}
				return u
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				if _, exists := dst.Object["status"]; exists {
					t.Errorf("destination still has .status: %v", dst.Object["status"])
				}
			},
		},
		{
			name: "preserves data/spec and user labels/annotations",
			source: func() *unstructured.Unstructured {
				u := newSourceCM("src-cm", "src-ns")
				u.Object["data"] = map[string]interface{}{
					"key": "value",
				}
				u.SetLabels(map[string]string{"user-label": "yes"})
				u.SetAnnotations(map[string]string{"user-annotation": "keep"})
				return u
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				data, ok := dst.Object["data"].(map[string]interface{})
				if !ok {
					t.Fatalf("destination .data missing or wrong type: %T", dst.Object["data"])
				}
				if data["key"] != "value" {
					t.Errorf(".data.key = %v, want %q", data["key"], "value")
				}
				if got := dst.GetLabels()["user-label"]; got != "yes" {
					t.Errorf("label user-label = %q, want %q", got, "yes")
				}
				if got := dst.GetAnnotations()["user-annotation"]; got != "keep" {
					t.Errorf("annotation user-annotation = %q, want %q", got, "keep")
				}
			},
		},
		{
			name: "strips kubectl last-applied-configuration",
			source: func() *unstructured.Unstructured {
				u := newSourceCM("src-cm", "src-ns")
				u.SetAnnotations(map[string]string{
					"kubectl.kubernetes.io/last-applied-configuration": `{"kind":"ConfigMap"}`,
					"keep-me": "please",
				})
				return u
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				ann := dst.GetAnnotations()
				if _, exists := ann["kubectl.kubernetes.io/last-applied-configuration"]; exists {
					t.Errorf("last-applied-configuration still present: %v", ann)
				}
				if got := ann["keep-me"]; got != "please" {
					t.Errorf("annotation keep-me = %q, want %q", got, "please")
				}
			},
		},
		{
			name: "overlay merges with source, overlay wins on conflict",
			source: func() *unstructured.Unstructured {
				u := newSourceCM("src-cm", "src-ns")
				u.SetLabels(map[string]string{"a": "source", "untouched": "yes"})
				u.SetAnnotations(map[string]string{"a": "source", "untouched": "yes"})
				return u
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{
				Labels:      map[string]string{"a": "overlay", "b": "new"},
				Annotations: map[string]string{"a": "overlay", "b": "new"},
			}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				wantLabels := map[string]string{
					"a":             "overlay",
					"b":             "new",
					"untouched":     "yes",
					ownedByUIDLabel: "00000000-0000-0000-0000-000000000001",
				}
				if diff := cmp.Diff(wantLabels, dst.GetLabels()); diff != "" {
					t.Errorf("labels mismatch (-want +got):\n%s", diff)
				}
				ann := dst.GetAnnotations()
				if got := ann["a"]; got != "overlay" {
					t.Errorf("annotation a = %q, want overlay", got)
				}
				if got := ann["b"]; got != "new" {
					t.Errorf("annotation b = %q, want new", got)
				}
				if got := ann["untouched"]; got != "yes" {
					t.Errorf("annotation untouched = %q, want yes", got)
				}
			},
		},
		{
			name: "overlay on empty source",
			source: func() *unstructured.Unstructured {
				return newSourceCM("src-cm", "src-ns")
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{
				Labels:      map[string]string{"only": "overlay"},
				Annotations: map[string]string{"only": "overlay"},
			}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				wantLabels := map[string]string{
					"only":          "overlay",
					ownedByUIDLabel: "00000000-0000-0000-0000-000000000001",
				}
				if diff := cmp.Diff(wantLabels, dst.GetLabels()); diff != "" {
					t.Errorf("labels mismatch (-want +got):\n%s", diff)
				}
				wantAnn := map[string]string{
					"only":            "overlay",
					ownedByAnnotation: "proj-ns/proj-name",
				}
				if diff := cmp.Diff(wantAnn, dst.GetAnnotations()); diff != "" {
					t.Errorf("annotations mismatch (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "ownership annotation stamped",
			source: func() *unstructured.Unstructured {
				return newSourceCM("src-cm", "src-ns")
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				got := dst.GetAnnotations()[ownedByAnnotation]
				want := "proj-ns/proj-name"
				if got != want {
					t.Errorf("%s = %q, want %q", ownedByAnnotation, got, want)
				}
			},
		},
		{
			// The UID label is what deleteAllOwnedDestinations (and the
			// future ensureDestWatch on the cluster reconciler) filter on;
			// without it those paths fall back to an O(all namespaces) scan.
			// Guard against accidental removal (#33).
			name: "ownership UID label stamped",
			source: func() *unstructured.Unstructured {
				return newSourceCM("src-cm", "src-ns")
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				got := dst.GetLabels()[ownedByUIDLabel]
				want := "00000000-0000-0000-0000-000000000001"
				if got != want {
					t.Errorf("%s = %q, want %q", ownedByUIDLabel, got, want)
				}
			},
		},
		{
			name: "destination defaulting",
			source: func() *unstructured.Unstructured {
				return newSourceCM("src-cm", "src-ns")
			},
			proj: newProj("proj-ns", "proj-name", baseSrc, projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				if got, want := dst.GetNamespace(), "proj-ns"; got != want {
					t.Errorf("namespace = %q, want %q", got, want)
				}
				if got, want := dst.GetName(), "src-cm"; got != want {
					t.Errorf("name = %q, want %q", got, want)
				}
			},
		},
		{
			name: "strips apiserver-allocated Service spec fields",
			source: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetAPIVersion("v1")
				u.SetKind("Service")
				u.SetName("src-svc")
				u.SetNamespace("src-ns")
				u.Object["spec"] = map[string]interface{}{
					"clusterIP":      "10.0.0.5",
					"clusterIPs":     []interface{}{"10.0.0.5"},
					"ipFamilies":     []interface{}{"IPv4"},
					"ipFamilyPolicy": "SingleStack",
					"selector":       map[string]interface{}{"app": "demo"},
					"ports": []interface{}{
						map[string]interface{}{
							"port":       int64(80),
							"targetPort": int64(8080),
						},
					},
				}
				return u
			},
			proj: newProj("proj-ns", "proj-name",
				projectionv1.SourceRef{Version: "v1", Kind: "Service", Name: "src-svc", Namespace: "src-ns"},
				projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				for _, path := range [][]string{
					{"spec", "clusterIP"},
					{"spec", "clusterIPs"},
					{"spec", "ipFamilies"},
					{"spec", "ipFamilyPolicy"},
				} {
					if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, path...); found {
						t.Errorf("expected %v to be stripped", path)
					}
				}
				// User-set spec fields must survive.
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "selector"); !found {
					t.Error("spec.selector was stripped, should have survived")
				}
				ports, found, _ := unstructured.NestedSlice(dst.Object, "spec", "ports")
				if !found {
					t.Fatal("spec.ports was stripped, should have survived")
				}
				if len(ports) != 1 {
					t.Errorf("spec.ports len = %d, want 1", len(ports))
				}
			},
		},
		{
			name: "strips apiserver-allocated PVC spec fields",
			source: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetAPIVersion("v1")
				u.SetKind("PersistentVolumeClaim")
				u.SetName("src-pvc")
				u.SetNamespace("src-ns")
				u.Object["spec"] = map[string]interface{}{
					"volumeName":       "pv-abc123",
					"storageClassName": "fast",
					"accessModes":      []interface{}{"ReadWriteOnce"},
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{
							"storage": "1Gi",
						},
					},
				}
				return u
			},
			proj: newProj("proj-ns", "proj-name",
				projectionv1.SourceRef{Version: "v1", Kind: "PersistentVolumeClaim", Name: "src-pvc", Namespace: "src-ns"},
				projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "volumeName"); found {
					t.Error("expected spec.volumeName to be stripped")
				}
				// storageClassName is user-set and must survive.
				sc, found, _ := unstructured.NestedString(dst.Object, "spec", "storageClassName")
				if !found {
					t.Error("spec.storageClassName was stripped, should have survived")
				}
				if sc != "fast" {
					t.Errorf("spec.storageClassName = %q, want %q", sc, "fast")
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "accessModes"); !found {
					t.Error("spec.accessModes was stripped, should have survived")
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "resources"); !found {
					t.Error("spec.resources was stripped, should have survived")
				}
			},
		},
		{
			name: "strips apiserver-allocated Pod spec fields",
			source: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetAPIVersion("v1")
				u.SetKind("Pod")
				u.SetName("src-pod")
				u.SetNamespace("src-ns")
				u.Object["spec"] = map[string]interface{}{
					"nodeName":     "node-1",
					"nodeSelector": map[string]interface{}{"disktype": "ssd"},
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "main",
							"image": "nginx",
						},
					},
				}
				return u
			},
			proj: newProj("proj-ns", "proj-name",
				projectionv1.SourceRef{Version: "v1", Kind: "Pod", Name: "src-pod", Namespace: "src-ns"},
				projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "nodeName"); found {
					t.Error("expected spec.nodeName to be stripped")
				}
				// nodeSelector is user-set and must survive.
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "nodeSelector"); !found {
					t.Error("spec.nodeSelector was stripped, should have survived")
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "containers"); !found {
					t.Error("spec.containers was stripped, should have survived")
				}
			},
		},
		{
			name: "strips apiserver-allocated Job spec fields",
			source: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetAPIVersion("batch/v1")
				u.SetKind("Job")
				u.SetName("src-job")
				u.SetNamespace("src-ns")
				u.Object["spec"] = map[string]interface{}{
					"selector": map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"batch.kubernetes.io/controller-uid": "abcd-1234",
						},
					},
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{
							"labels": map[string]interface{}{
								"controller-uid":                     "abcd-1234",
								"batch.kubernetes.io/controller-uid": "abcd-1234",
								"batch.kubernetes.io/job-name":       "src-job",
								"user-label":                         "keep-me",
							},
						},
						"spec": map[string]interface{}{
							"containers": []interface{}{
								map[string]interface{}{
									"name":  "main",
									"image": "busybox",
								},
							},
							"restartPolicy": "Never",
						},
					},
					"backoffLimit": int64(4),
				}
				return u
			},
			proj: newProj("proj-ns", "proj-name",
				projectionv1.SourceRef{Group: "batch", Version: "v1", Kind: "Job", Name: "src-job", Namespace: "src-ns"},
				projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				// spec.selector must be stripped so the destination apiserver regenerates it.
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "selector"); found {
					t.Error("expected spec.selector to be stripped for Job")
				}
				// Auto-generated labels on the pod template must be stripped.
				for _, key := range []string{
					"controller-uid",
					"batch.kubernetes.io/controller-uid",
					"batch.kubernetes.io/job-name",
				} {
					if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "template", "metadata", "labels", key); found {
						t.Errorf("expected spec.template.metadata.labels[%q] to be stripped for Job", key)
					}
				}
				// User-set template labels must survive.
				userLabel, found, _ := unstructured.NestedString(dst.Object, "spec", "template", "metadata", "labels", "user-label")
				if !found {
					t.Error("spec.template.metadata.labels[user-label] was stripped, should have survived")
				}
				if userLabel != "keep-me" {
					t.Errorf("user-label = %q, want %q", userLabel, "keep-me")
				}
				// User-set spec fields must survive.
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "backoffLimit"); !found {
					t.Error("spec.backoffLimit was stripped, should have survived")
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "template", "spec", "containers"); !found {
					t.Error("spec.template.spec.containers was stripped, should have survived")
				}
			},
		},
		{
			name: "leaves non-listed Kinds untouched",
			source: func() *unstructured.Unstructured {
				u := &unstructured.Unstructured{}
				u.SetAPIVersion("apps/v1")
				u.SetKind("Deployment")
				u.SetName("src-dep")
				u.SetNamespace("src-ns")
				u.Object["spec"] = map[string]interface{}{
					"replicas": int64(3),
					"selector": map[string]interface{}{
						"matchLabels": map[string]interface{}{"app": "demo"},
					},
				}
				return u
			},
			proj: newProj("proj-ns", "proj-name",
				projectionv1.SourceRef{Group: "apps", Version: "v1", Kind: "Deployment", Name: "src-dep", Namespace: "src-ns"},
				projectionv1.ProjectionDestination{}, projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				replicas, found, _ := unstructured.NestedInt64(dst.Object, "spec", "replicas")
				if !found {
					t.Fatal("spec.replicas was stripped for Deployment, should have survived")
				}
				if replicas != 3 {
					t.Errorf("spec.replicas = %d, want 3", replicas)
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(dst.Object, "spec", "selector"); !found {
					t.Error("spec.selector was stripped for Deployment, should have survived")
				}
			},
		},
		{
			name: "explicit destination name override",
			source: func() *unstructured.Unstructured {
				return newSourceCM("src-cm", "src-ns")
			},
			// Destination namespace is always the Projection's own namespace
			// for v0.3 (cross-namespace mirroring lives on ClusterProjection);
			// the rename override only touches the destination name.
			proj: newProj("proj-ns", "proj-name", baseSrc,
				projectionv1.ProjectionDestination{Name: "other-name"},
				projectionv1.Overlay{}),
			asserts: func(t *testing.T, dst *unstructured.Unstructured) {
				if got, want := dst.GetNamespace(), "proj-ns"; got != want {
					t.Errorf("namespace = %q, want %q", got, want)
				}
				if got, want := dst.GetName(), "other-name"; got != want {
					t.Errorf("name = %q, want %q", got, want)
				}
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			targetNS, _ := destinationCoords(tc.proj)
			dst := buildDestination(tc.source(), tc.proj, targetNS)
			if dst == nil {
				t.Fatal("buildDestination returned nil")
			}
			tc.asserts(t, dst)
		})
	}
}
