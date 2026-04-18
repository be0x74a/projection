// Package main is the projection benchmark harness. Runs against an
// already-provisioned Kubernetes cluster (see `make bench`).
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	profileName := flag.String("profile", "", "operating-point profile: small, medium, selector, full, custom")
	kubeconfig := flag.String("kubeconfig", "", "path to kubeconfig (required; default-lookup is refused for safety)")
	allowDefault := flag.Bool("yes-i-know-this-is-a-test-cluster", false, "bypass the default-kubeconfig safety gate")
	output := flag.String("output", "text", "output format: text or json")
	flag.Int("projections", 0, "custom profile: number of Projections")
	flag.Int("gvks", 0, "custom profile: number of distinct source GVKs")
	flag.Int("namespaces", 0, "custom profile: number of source+dest namespaces")
	flag.Int("selector-ns", 0, "custom profile: matching namespaces for selector profile")
	flag.Parse()

	if err := run(*profileName, *kubeconfig, *allowDefault, *output); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
}

func run(profileName, kubeconfig string, allowDefault bool, output string) error {
	if profileName == "" {
		return fmt.Errorf("--profile is required")
	}
	// Subsequent tasks wire profile parsing, safety gate, and the runner.
	return fmt.Errorf("not yet implemented")
}
