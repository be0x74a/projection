package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckSafety(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot find home: %v", err)
	}
	defaultPath := filepath.Join(home, ".kube", "config")

	tests := []struct {
		name         string
		kubeconfig   string
		allowDefault bool
		wantErr      bool
	}{
		{"empty_kubeconfig_rejected", "", false, true},
		{"default_path_rejected", defaultPath, false, true},
		{"default_path_with_bypass_allowed", defaultPath, true, false},
		{"explicit_other_path_allowed", "/tmp/kind-bench.kubeconfig", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSafety(tc.kubeconfig, tc.allowDefault)
			if tc.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
