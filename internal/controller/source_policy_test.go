/*
Copyright 2024.

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

func TestCheckSourceProjectable(t *testing.T) {
	src := func(annotationValue string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("v1")
		u.SetKind("ConfigMap")
		u.SetName("src")
		u.SetNamespace("ns")
		if annotationValue != "<absent>" {
			u.SetAnnotations(map[string]string{projectableAnnotation: annotationValue})
		}
		return u
	}

	tests := []struct {
		name       string
		mode       SourceMode
		annotation string // "<absent>" means the annotation is missing entirely
		wantOK     bool
		wantReason string
	}{
		{"permissive + no annotation → allowed", SourceModePermissive, "<absent>", true, ""},
		{"permissive + \"true\" → allowed", SourceModePermissive, "true", true, ""},
		{"permissive + arbitrary value → allowed", SourceModePermissive, "yes", true, ""},
		{"permissive + \"false\" → blocked (opt-out)", SourceModePermissive, "false", false, "SourceOptedOut"},

		{"allowlist + no annotation → blocked", SourceModeAllowlist, "<absent>", false, "SourceNotProjectable"},
		{"allowlist + empty string → blocked", SourceModeAllowlist, "", false, "SourceNotProjectable"},
		{"allowlist + \"true\" → allowed", SourceModeAllowlist, "true", true, ""},
		{"allowlist + \"false\" → blocked (opt-out wins over lack of opt-in)", SourceModeAllowlist, "false", false, "SourceOptedOut"},
		{"allowlist + other value → blocked as not-projectable", SourceModeAllowlist, "yes", false, "SourceNotProjectable"},

		// Empty mode string must default to allowlist (matches what
		// SetupWithManager does at startup via the CLI flag default).
		{"empty mode defaults to allowlist + missing annotation → blocked", "", "<absent>", false, "SourceNotProjectable"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &ProjectionReconciler{SourceMode: tc.mode}
			reason, msg, ok := r.checkSourceProjectable(src(tc.annotation))
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v (reason=%q msg=%q)", ok, tc.wantOK, reason, msg)
			}
			if !tc.wantOK && reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
			// Messages should be populated when blocked, empty when allowed.
			if !tc.wantOK && msg == "" {
				t.Error("expected non-empty message when blocked")
			}
			if tc.wantOK && (reason != "" || msg != "") {
				t.Errorf("expected empty reason/msg when allowed; got reason=%q msg=%q", reason, msg)
			}
		})
	}
}
