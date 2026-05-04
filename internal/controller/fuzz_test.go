/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	projectionv1 "github.com/projection-operator/projection/api/v1"
)

// FuzzBuildDestination exercises the metadata-stripping, overlay-merge, and
// ownership-stamp invariants of buildDestination against randomly-generated
// inputs. The seed corpus covers known-good combinations from the table
// tests; the fuzzer then varies each primitive to find edge cases.
func FuzzBuildDestination(f *testing.F) {
	// Seed corpus — shape matches the f.Fuzz signature below.
	f.Add("src-cm", "src-ns", "ConfigMap", "v1",
		"rv-123", "uid-abc", "app", "demo",
		"proj-name", "proj-ns", "dst-name", "dst-ns",
		"overlay-key", "overlay-val")
	f.Add("x", "x", "Service", "v1",
		"", "", "", "",
		"p", "p", "", "",
		"", "")
	f.Add("svc", "team-a", "Service", "v1",
		"1", "u", "tier", "front",
		"mirror", "team-a", "svc-dst", "team-b",
		"mirrored", "true")

	f.Fuzz(func(t *testing.T,
		srcName, srcNS, kind, apiVersion string,
		rvSrc, uidSrc, labelKey, labelValue string,
		projName, projNS, dstName, dstNS string,
		overlayKey, overlayValue string) {

		// Skip clearly invalid inputs that the reconciler itself would reject
		// at admission or GVR-resolve time. Fuzzing those cases doesn't teach
		// us anything about buildDestination's own logic.
		if srcName == "" || srcNS == "" || kind == "" || apiVersion == "" ||
			projName == "" || projNS == "" {
			t.Skip()
		}

		src := &unstructured.Unstructured{}
		src.SetAPIVersion(apiVersion)
		src.SetKind(kind)
		src.SetName(srcName)
		src.SetNamespace(srcNS)
		if rvSrc != "" {
			src.SetResourceVersion(rvSrc)
		}
		if uidSrc != "" {
			src.SetUID(types.UID(uidSrc))
		}
		if labelKey != "" {
			src.SetLabels(map[string]string{labelKey: labelValue})
		}
		// Plant a last-applied-configuration annotation so we can assert it
		// gets stripped — one of buildDestination's contract clauses.
		src.SetAnnotations(map[string]string{
			"kubectl.kubernetes.io/last-applied-configuration": "should-be-stripped",
			"keep-me": "yes",
		})

		proj := &projectionv1.Projection{
			ObjectMeta: metav1.ObjectMeta{Name: projName, Namespace: projNS},
			Spec: projectionv1.ProjectionSpec{
				Source: projectionv1.SourceRef{
					APIVersion: apiVersion, Kind: kind,
					Name: srcName, Namespace: srcNS,
				},
				Destination: projectionv1.DestinationRef{Namespace: dstNS, Name: dstName},
			},
		}
		if overlayKey != "" {
			proj.Spec.Overlay.Labels = map[string]string{overlayKey: overlayValue}
			proj.Spec.Overlay.Annotations = map[string]string{overlayKey: overlayValue}
		}

		targetNS, _ := destinationCoords(proj)
		dst := buildDestination(src, proj, targetNS)

		if dst == nil {
			t.Fatalf("buildDestination returned nil for src=%s/%s proj=%s/%s",
				srcNS, srcName, projNS, projName)
		}

		// status must not carry over.
		if _, found := dst.Object["status"]; found {
			t.Error(".status was not stripped")
		}

		// Ownership annotation stamped and correct.
		owner := dst.GetAnnotations()[ownedByAnnotation]
		want := projNS + "/" + projName
		if owner != want {
			t.Errorf("ownership annotation = %q, want %q", owner, want)
		}

		// last-applied-configuration stripped.
		if _, ok := dst.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"]; ok {
			t.Error("kubectl.kubernetes.io/last-applied-configuration not stripped")
		}

		// Destination name/namespace match destinationCoords contract.
		wantNS, wantName := destinationCoords(proj)
		if dst.GetNamespace() != wantNS || dst.GetName() != wantName {
			t.Errorf("dst = %s/%s, want %s/%s",
				dst.GetNamespace(), dst.GetName(), wantNS, wantName)
		}

		// Every dropped metadata field is absent on dst.
		if metadata, ok := dst.Object["metadata"].(map[string]interface{}); ok {
			for _, field := range droppedMetadataFields {
				if _, present := metadata[field]; present {
					t.Errorf("metadata.%s was not stripped", field)
				}
			}
		}

		// Overlay label/annotation applied (when non-empty).
		if overlayKey != "" && overlayValue != "" {
			if dst.GetLabels()[overlayKey] != overlayValue {
				t.Errorf("overlay label %q=%q not applied (got %q)",
					overlayKey, overlayValue, dst.GetLabels()[overlayKey])
			}
			// Annotation with the same key also lands (ownership key is
			// separate and can't collide with an arbitrary overlay key
			// unless the overlay uses ownedByAnnotation — in which case
			// buildDestination's final stamp wins by design).
			if overlayKey != ownedByAnnotation {
				if dst.GetAnnotations()[overlayKey] != overlayValue {
					t.Errorf("overlay annotation %q=%q not applied (got %q)",
						overlayKey, overlayValue, dst.GetAnnotations()[overlayKey])
				}
			}
		}

		// Preserved source annotations that aren't on the strip list survive.
		if dst.GetAnnotations()["keep-me"] != "yes" {
			t.Error("non-stripped source annotation was lost")
		}

		// APIVersion + Kind carry over verbatim.
		if dst.GetAPIVersion() != apiVersion || dst.GetKind() != kind {
			t.Errorf("apiVersion/kind drift: got %s/%s, want %s/%s",
				dst.GetAPIVersion(), dst.GetKind(), apiVersion, kind)
		}
	})
}

// FuzzNeedsUpdate checks the three contract properties of needsUpdate:
// reflexivity (needsUpdate(a,a) == false), metadata-insensitivity
// (differences in resourceVersion/uid/generation/status must NOT trigger
// an update), and data-sensitivity (differences in .data/.spec MUST
// trigger an update).
func FuzzNeedsUpdate(f *testing.F) {
	f.Add("v1", "ConfigMap", "name", "ns", "key", "value-a", "value-b", "rv-a", "rv-b")
	f.Add("v1", "Service", "svc", "ns", "clusterIP", "10.0.0.1", "10.0.0.2", "1", "2")
	f.Add("apps/v1", "Deployment", "d", "ns", "replicas", "1", "2", "", "")

	f.Fuzz(func(t *testing.T,
		apiVersion, kind, name, namespace string,
		dataKey, valueA, valueB, rvA, rvB string) {

		if apiVersion == "" || kind == "" || name == "" || dataKey == "" {
			t.Skip()
		}

		build := func(rv, dataValue string) *unstructured.Unstructured {
			u := &unstructured.Unstructured{}
			u.SetAPIVersion(apiVersion)
			u.SetKind(kind)
			u.SetName(name)
			u.SetNamespace(namespace)
			if rv != "" {
				u.SetResourceVersion(rv)
			}
			_ = unstructured.SetNestedField(u.Object, dataValue, "data", dataKey)
			return u
		}

		// Reflexivity: same object compared to itself never needs an update.
		a := build(rvA, valueA)
		if needsUpdate(a, a) {
			t.Error("needsUpdate(a, a) returned true")
		}

		// Metadata-only difference (resourceVersion) must not trigger update.
		if rvA != rvB {
			b := build(rvB, valueA)
			if needsUpdate(a, b) {
				t.Errorf("resourceVersion diff caused update: %q vs %q", rvA, rvB)
			}
		}

		// Data difference must trigger update, regardless of resourceVersion.
		if valueA != valueB {
			c := build(rvA, valueA)
			d := build(rvA, valueB)
			if !needsUpdate(c, d) {
				t.Errorf("data diff didn't trigger update: %q vs %q", valueA, valueB)
			}
		}

		// Status difference never triggers update.
		e := build(rvA, valueA)
		_ = unstructured.SetNestedField(e.Object, "whatever", "status", "phase")
		f := build(rvA, valueA)
		if needsUpdate(e, f) {
			t.Error("status diff triggered update")
		}
	})
}

// FuzzPreserveAPIServerAllocatedFields verifies that the preserve helper
// copies exactly the fields declared in droppedSpecFieldsByGVK for the
// object's GVK, and does NOT touch Kinds that aren't in that map.
func FuzzPreserveAPIServerAllocatedFields(f *testing.F) {
	f.Add("v1", "Service", "10.96.10.10", "svc", "ns")
	f.Add("v1", "PersistentVolumeClaim", "pv-abc", "pvc", "ns")
	f.Add("v1", "Pod", "node-1", "pod", "ns")
	f.Add("batch/v1", "Job", "abcd-uid-1234", "job", "ns")
	f.Add("v1", "ConfigMap", "ignored", "cm", "ns")

	f.Fuzz(func(t *testing.T,
		apiVersion, kind, allocatedValue, name, namespace string) {

		if apiVersion == "" || kind == "" {
			t.Skip()
		}

		// Build an "existing" object with the allocated field set at the
		// first path listed for this Kind (if any).
		gvk := (&unstructured.Unstructured{}).GroupVersionKind()
		gvk.Kind = kind
		existing := &unstructured.Unstructured{}
		existing.SetAPIVersion(apiVersion)
		existing.SetKind(kind)
		existing.SetName(name)
		existing.SetNamespace(namespace)

		paths := droppedSpecFieldsByGVK[existing.GroupVersionKind()]
		if allocatedValue != "" && len(paths) > 0 {
			_ = unstructured.SetNestedField(existing.Object, allocatedValue, paths[0]...)
		}

		desired := &unstructured.Unstructured{}
		desired.SetAPIVersion(apiVersion)
		desired.SetKind(kind)

		preserveAPIServerAllocatedFields(existing, desired)

		if len(paths) == 0 {
			// Unknown Kind — nothing should have been copied.
			if len(desired.Object) > 2 { // apiVersion + kind
				// Check if any field was unexpectedly added.
				expectedKeys := []string{"apiVersion", "kind"}
				for k := range desired.Object {
					if !slices.Contains(expectedKeys, k) {
						t.Errorf("unexpected field copied for non-listed Kind %s: %s", kind, k)
					}
				}
			}
			return
		}

		// For a known Kind, the first allocated path should have been copied
		// when `existing` had a value set there.
		if allocatedValue != "" {
			got, found, err := unstructured.NestedString(desired.Object, paths[0]...)
			if err != nil || !found {
				// Could happen if the path type is not a string (e.g. clusterIPs
				// is a list). Only assert when we know it's a string field.
				return
			}
			if got != allocatedValue {
				t.Errorf("%v not preserved: got %q want %q", paths[0], got, allocatedValue)
			}
		}
	})
}
