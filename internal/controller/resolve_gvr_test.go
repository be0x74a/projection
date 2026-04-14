/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"strings"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	projectionv1 "github.com/be0x74a/projection/api/v1"
)

// TestResolveGVRRejectsClusterScoped is a focused unit test — builds a
// RESTMapper by hand so we don't need envtest for this single shape check.
func TestResolveGVRRejectsClusterScoped(t *testing.T) {
	mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, apimeta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, apimeta.RESTScopeRoot)
	mapper.Add(schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}, apimeta.RESTScopeRoot)

	tests := []struct {
		name        string
		src         projectionv1.SourceRef
		wantErr     bool
		errContains string
	}{
		{
			name: "namespaced resource resolves",
			src:  projectionv1.SourceRef{APIVersion: "v1", Kind: "ConfigMap", Name: "x", Namespace: "ns"},
		},
		{
			name:        "Namespace is rejected as cluster-scoped",
			src:         projectionv1.SourceRef{APIVersion: "v1", Kind: "Namespace", Name: "foo", Namespace: "whatever"},
			wantErr:     true,
			errContains: "cluster-scoped",
		},
		{
			name:        "ClusterRole is rejected as cluster-scoped",
			src:         projectionv1.SourceRef{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Name: "admin", Namespace: "whatever"},
			wantErr:     true,
			errContains: "cluster-scoped",
		},
		{
			name:        "unknown Kind surfaces the RESTMapper error",
			src:         projectionv1.SourceRef{APIVersion: "v1", Kind: "FleetOfUnicorns", Name: "x", Namespace: "ns"},
			wantErr:     true,
			errContains: "resolving",
		},
	}

	r := &ProjectionReconciler{RESTMapper: mapper}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.resolveGVR(tc.src)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("error %q should contain %q", err.Error(), tc.errContains)
			}
		})
	}
}
