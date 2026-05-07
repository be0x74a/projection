// Package main is the projection benchmark harness. Runs against an
// already-provisioned Kubernetes cluster (see `make bench`).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	profileName := flag.String("profile", "",
		"operating-point profile: "+validProfileNames)
	kubeconfig := flag.String("kubeconfig", "", "path to kubeconfig (required; default-lookup is refused for safety)")
	allowDefault := flag.Bool("yes-i-know-this-is-a-test-cluster", false, "bypass the default-kubeconfig safety gate")
	output := flag.String("output", "text", "output format: text or json")
	namespacedProjections := flag.Int("namespaced-projections", 0,
		"custom profile: number of namespaced Projection CRs (single-target shape)")
	selectorNamespaces := flag.Int("selector-namespaces", 0,
		"custom profile: number of label-matched namespaces for the CP-selector instance")
	listNamespaces := flag.Int("list-namespaces", 0,
		"custom profile: number of explicit-list namespaces for the CP-list instance")
	gvks := flag.Int("gvks", 0, "custom profile: number of distinct source GVKs")
	namespaces := flag.Int("namespaces", 0, "custom profile: number of NP source+dest namespaces")
	metricsURL := flag.String("metrics-url", "http://127.0.0.1:8080/metrics",
		"controller metrics endpoint; defaults to the `make run` shell's plain-http bind")
	flag.Parse()

	overrides := ProfileOverrides{
		NamespacedProjections: *namespacedProjections,
		SelectorNamespaces:    *selectorNamespaces,
		ListNamespaces:        *listNamespaces,
		GVKs:                  *gvks,
		Namespaces:            *namespaces,
	}

	if err := run(*profileName, *kubeconfig, *allowDefault, *output, *metricsURL, overrides); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
}

func run(profileName, kubeconfig string, allowDefault bool, output, metricsURL string, overrides ProfileOverrides) error {
	if profileName == "" {
		return fmt.Errorf("--profile is required")
	}
	if err := CheckSafety(kubeconfig, allowDefault); err != nil {
		return err
	}
	c, err := buildClients(kubeconfig)
	if err != nil {
		return err
	}

	ctx, cancel := signalAwareContext()
	defer cancel()

	var profiles []Profile
	switch profileName {
	case "full":
		profiles = ExpandFull()
	case "fast":
		profiles, err = ExpandFast()
		if err != nil {
			return err
		}
	default:
		p, perr := ParseProfile(profileName, overrides)
		if perr != nil {
			return perr
		}
		profiles = []Profile{p}
	}

	reports := make([]*Report, 0, len(profiles))
	for _, p := range profiles {
		_, _ = fmt.Fprintf(os.Stderr, "=> running profile %q\n", p.Name)
		r, err := runProfile(ctx, c, p, metricsURL)
		if err != nil {
			return fmt.Errorf("profile %s: %w", p.Name, err)
		}
		reports = append(reports, r)
	}
	return writeReports(os.Stdout, output, reports)
}

// signalAwareContext returns a context that is cancelled on SIGINT/SIGTERM so
// defer teardown in runProfile still runs on Ctrl-C.
func signalAwareContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}
