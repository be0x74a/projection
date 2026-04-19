package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckSafety enforces the "harness only runs against opt-in clusters" rule.
// The harness is destructive: it creates hundreds to thousands of objects
// and deletes them. We refuse to run against the user's default kubeconfig
// unless they pass the explicit bypass flag.
func CheckSafety(kubeconfig string, allowDefault bool) error {
	if kubeconfig == "" {
		return fmt.Errorf("--kubeconfig is required; default-lookup is refused for safety " +
			"(pass --yes-i-know-this-is-a-test-cluster to override)")
	}
	home, err := os.UserHomeDir()
	if err == nil {
		defaultPath := filepath.Join(home, ".kube", "config")
		if kubeconfig == defaultPath && !allowDefault {
			return fmt.Errorf("refusing to run against default kubeconfig %q "+
				"(pass --yes-i-know-this-is-a-test-cluster to override)", kubeconfig)
		}
	}
	return nil
}
