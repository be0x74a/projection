/*
Copyright 2026 The projection Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

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
