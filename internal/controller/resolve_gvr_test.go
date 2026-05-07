/*
Copyright 2026 The projection Authors.

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

	projectionv1 "github.com/projection-operator/projection/api/v1"
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
			src:  projectionv1.SourceRef{Version: "v1", Kind: "ConfigMap", Name: "x", Namespace: "ns"},
		},
		{
			name:        "Namespace is rejected as cluster-scoped",
			src:         projectionv1.SourceRef{Version: "v1", Kind: "Namespace", Name: "foo", Namespace: "whatever"},
			wantErr:     true,
			errContains: "cluster-scoped",
		},
		{
			name:        "ClusterRole is rejected as cluster-scoped",
			src:         projectionv1.SourceRef{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole", Name: "admin", Namespace: "whatever"},
			wantErr:     true,
			errContains: "cluster-scoped",
		},
		{
			name:        "unknown Kind surfaces the RESTMapper error",
			src:         projectionv1.SourceRef{Version: "v1", Kind: "FleetOfUnicorns", Name: "x", Namespace: "ns"},
			wantErr:     true,
			errContains: "resolving",
		},
	}

	r := &ProjectionReconciler{ControllerDeps: &ControllerDeps{RESTMapper: mapper}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := r.resolveGVR(tc.src)
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

// TestResolveGVRPreferredVersion exercises the * sentinel — the unpinned form.
// Builds a DefaultRESTMapper that knows two versions of apps/Deployment so we
// can assert the preferred-version pick is deterministic (the first
// GroupVersion in the constructor slice is preferred).
func TestResolveGVRPreferredVersion(t *testing.T) {
	mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{
		{Group: "apps", Version: "v1"},
		{Group: "apps", Version: "v1beta2"},
	})
	mapper.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, apimeta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "apps", Version: "v1beta2", Kind: "Deployment"}, apimeta.RESTScopeNamespace)

	r := &ProjectionReconciler{ControllerDeps: &ControllerDeps{RESTMapper: mapper}}

	t.Run("pinned form returns the pinned version", func(t *testing.T) {
		gvr, version, err := r.resolveGVR(projectionv1.SourceRef{
			Group: "apps", Version: "v1beta2", Kind: "Deployment",
			Name: "x", Namespace: "y",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gvr.Version != "v1beta2" {
			t.Errorf("gvr.Version = %q, want v1beta2", gvr.Version)
		}
		if version != "v1beta2" {
			t.Errorf("version = %q, want v1beta2", version)
		}
	})

	t.Run("unpinned form returns the RESTMapper-preferred version", func(t *testing.T) {
		gvr, version, err := r.resolveGVR(projectionv1.SourceRef{
			Group: "apps", Kind: "Deployment",
			Name: "x", Namespace: "y",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// DefaultRESTMapper preferred = first GroupVersion in the constructor slice.
		if gvr.Version != "v1" {
			t.Errorf("gvr.Version = %q, want v1 (preferred)", gvr.Version)
		}
		if version != "v1" {
			t.Errorf("version = %q, want v1", version)
		}
	})
}

func TestResolvedVersionMessage(t *testing.T) {
	cases := []struct {
		name            string
		group           string
		version         string
		kind            string
		resolvedVersion string
		want            string
	}{
		{
			name:            "unpinned with resolved version",
			group:           "apps",
			version:         "",
			kind:            "Deployment",
			resolvedVersion: "v1",
			want:            "resolved apps/Deployment to preferred version v1",
		},
		{
			name:    "pinned returns empty",
			group:   "apps",
			version: "v1",
			kind:    "Deployment",
			// resolvedVersion unused for pinned forms
			want: "",
		},
		{
			name:    "core pinned returns empty",
			version: "v1",
			kind:    "ConfigMap",
			want:    "",
		},
		{
			name:            "unpinned with empty resolvedVersion returns empty",
			group:           "apps",
			version:         "",
			kind:            "Deployment",
			resolvedVersion: "",
			// Guards against failDestination calls that happen before
			// resolveGVR runs (e.g. a future early-failure path).
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvedVersionMessage(projectionv1.SourceRef{
				Group:   tc.group,
				Version: tc.version,
				Kind:    tc.kind,
			}, tc.resolvedVersion)
			if got != tc.want {
				t.Errorf("resolvedVersionMessage(%q/%q, %q) = %q, want %q",
					tc.group, tc.version, tc.resolvedVersion, got, tc.want)
			}
		})
	}
}
