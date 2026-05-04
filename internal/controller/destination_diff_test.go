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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestNeedsUpdate(t *testing.T) {
	base := func() *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("v1")
		u.SetKind("ConfigMap")
		u.SetName("dst")
		u.SetNamespace("ns")
		u.SetLabels(map[string]string{"a": "1"})
		u.SetAnnotations(map[string]string{"k": "v"})
		_ = unstructured.SetNestedField(u.Object, "x", "data", "key")
		return u
	}

	tests := []struct {
		name   string
		mutate func(existing, desired *unstructured.Unstructured)
		want   bool
	}{
		{
			name:   "identical objects do not need update",
			mutate: func(existing, desired *unstructured.Unstructured) {},
			want:   false,
		},
		{
			name: "label difference triggers update",
			mutate: func(existing, desired *unstructured.Unstructured) {
				desired.SetLabels(map[string]string{"a": "2"})
			},
			want: true,
		},
		{
			name: "annotation difference triggers update",
			mutate: func(existing, desired *unstructured.Unstructured) {
				desired.SetAnnotations(map[string]string{"k": "different"})
			},
			want: true,
		},
		{
			name: "data field difference triggers update",
			mutate: func(existing, desired *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(desired.Object, "y", "data", "key")
			},
			want: true,
		},
		{
			name: "server-only metadata differences do not trigger update",
			mutate: func(existing, desired *unstructured.Unstructured) {
				existing.SetResourceVersion("12345")
				existing.SetUID("abc-123")
				existing.SetGeneration(7)
			},
			want: false,
		},
		{
			name: "status difference does not trigger update",
			mutate: func(existing, desired *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(existing.Object, "phase", "status", "phase")
			},
			want: false,
		},
		{
			name: "extra top-level field on existing triggers update",
			mutate: func(existing, desired *unstructured.Unstructured) {
				_ = unstructured.SetNestedField(existing.Object, "v", "spec", "field")
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			existing := base()
			desired := base()
			tc.mutate(existing, desired)
			if got := needsUpdate(existing, desired); got != tc.want {
				t.Errorf("needsUpdate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPreserveAPIServerAllocatedFields(t *testing.T) {
	t.Run("Service clusterIP carried from existing to desired", func(t *testing.T) {
		existing := &unstructured.Unstructured{}
		existing.SetAPIVersion("v1")
		existing.SetKind("Service")
		_ = unstructured.SetNestedField(existing.Object, "10.96.10.10", "spec", "clusterIP")
		_ = unstructured.SetNestedStringSlice(existing.Object, []string{"10.96.10.10"}, "spec", "clusterIPs")

		desired := &unstructured.Unstructured{}
		desired.SetAPIVersion("v1")
		desired.SetKind("Service")
		// desired.spec has no clusterIP — buildDestination strips it.
		_ = unstructured.SetNestedField(desired.Object, int64(80), "spec", "ports", "port")

		preserveAPIServerAllocatedFields(existing, desired)

		got, found, _ := unstructured.NestedString(desired.Object, "spec", "clusterIP")
		if !found || got != "10.96.10.10" {
			t.Errorf("clusterIP not preserved: found=%v got=%q", found, got)
		}
	})

	t.Run("Kind not in map: nothing is copied", func(t *testing.T) {
		existing := &unstructured.Unstructured{}
		existing.SetAPIVersion("v1")
		existing.SetKind("ConfigMap")
		_ = unstructured.SetNestedField(existing.Object, "x", "spec", "weird")

		desired := &unstructured.Unstructured{}
		desired.SetAPIVersion("v1")
		desired.SetKind("ConfigMap")

		preserveAPIServerAllocatedFields(existing, desired)

		if _, found, _ := unstructured.NestedString(desired.Object, "spec", "weird"); found {
			t.Errorf("non-listed Kind should not have fields copied")
		}
	})
}
