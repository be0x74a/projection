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

	r := &ProjectionReconciler{RESTMapper: mapper}

	t.Run("pinned form returns the pinned version", func(t *testing.T) {
		gvr, version, err := r.resolveGVR(projectionv1.SourceRef{
			APIVersion: "apps/v1beta2", Kind: "Deployment",
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
			APIVersion: "apps/*", Kind: "Deployment",
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

	t.Run("bare * without group is rejected", func(t *testing.T) {
		_, _, err := r.resolveGVR(projectionv1.SourceRef{
			APIVersion: "*", Kind: "Deployment",
			Name: "x", Namespace: "y",
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "group is required") {
			t.Errorf("error %q should contain 'group is required'", err.Error())
		}
	})
}

func TestResolvedVersionMessage(t *testing.T) {
	cases := []struct {
		name            string
		apiVersion      string
		kind            string
		resolvedVersion string
		want            string
	}{
		{
			name:            "unpinned with resolved version",
			apiVersion:      "apps/*",
			kind:            "Deployment",
			resolvedVersion: "v1",
			want:            "resolved apps/Deployment to preferred version v1",
		},
		{
			name:       "pinned returns empty",
			apiVersion: "apps/v1",
			kind:       "Deployment",
			// resolvedVersion unused for pinned forms
			want: "",
		},
		{
			name:       "core pinned returns empty",
			apiVersion: "v1",
			kind:       "ConfigMap",
			want:       "",
		},
		{
			name:            "unpinned with empty resolvedVersion returns empty",
			apiVersion:      "apps/*",
			kind:            "Deployment",
			resolvedVersion: "",
			// Guards against failDestination calls that happen before
			// resolveGVR runs (e.g. InvalidSpec mutex violation).
			want: "",
		},
		{
			name:       "malformed apiVersion returns empty",
			apiVersion: "apps//v1",
			kind:       "Deployment",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvedVersionMessage(projectionv1.SourceRef{
				APIVersion: tc.apiVersion,
				Kind:       tc.kind,
			}, tc.resolvedVersion)
			if got != tc.want {
				t.Errorf("resolvedVersionMessage(%q, %q) = %q, want %q",
					tc.apiVersion, tc.resolvedVersion, got, tc.want)
			}
		})
	}
}
