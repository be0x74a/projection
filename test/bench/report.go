package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// Report is the harness's per-profile output.
type Report struct {
	Profile         Profile      `json:"profile"`
	Environment     Environment  `json:"environment"`
	Measurements    Measurements `json:"measurements"`
	DurationSeconds float64      `json:"duration_seconds"`
}

type Environment struct {
	KubeconfigHost string `json:"kubeconfig_host"`
	Timestamp      string `json:"timestamp_utc"`
	GoVersion      string `json:"go_version,omitempty"`
	OSArch         string `json:"os_arch,omitempty"`
}

type Measurements struct {
	WatchedGVKs        float64 `json:"watched_gvks"`
	ControllerHeapMB   float64 `json:"controller_heap_mb"`
	ControllerRSSMB    float64 `json:"controller_rss_mb"`
	ControllerCPUDelta float64 `json:"controller_cpu_seconds_delta"`
	ReconcileP50Ms     float64 `json:"reconcile_p50_ms"`
	ReconcileP95Ms     float64 `json:"reconcile_p95_ms"`
	ReconcileP99Ms     float64 `json:"reconcile_p99_ms"`

	// Non-selector profiles: one E2E latency distribution across sampled
	// Projections. Zero for selector profiles.
	E2ESamples int           `json:"e2e_samples,omitempty"`
	E2EP50     time.Duration `json:"e2e_p50_ns,omitempty"`
	E2EP95     time.Duration `json:"e2e_p95_ns,omitempty"`
	E2EP99     time.Duration `json:"e2e_p99_ns,omitempty"`

	// Selector profiles only: two distributions, one for the first-listed
	// destination namespace and one for the last-listed. The spread between
	// First and Last exposes fan-out cost. Zero for non-selector profiles.
	E2EFirstNsSamples int           `json:"e2e_first_ns_samples,omitempty"`
	E2EFirstNsP50     time.Duration `json:"e2e_first_ns_p50_ns,omitempty"`
	E2EFirstNsP95     time.Duration `json:"e2e_first_ns_p95_ns,omitempty"`
	E2EFirstNsP99     time.Duration `json:"e2e_first_ns_p99_ns,omitempty"`
	E2ELastNsP50      time.Duration `json:"e2e_last_ns_p50_ns,omitempty"`
	E2ELastNsP95      time.Duration `json:"e2e_last_ns_p95_ns,omitempty"`
	E2ELastNsP99      time.Duration `json:"e2e_last_ns_p99_ns,omitempty"`
}

func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func (r *Report) WriteText(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "profile\t%s\n", r.Profile.Name)
	fmt.Fprintf(tw, "projections\t%d\n", r.Profile.Projections)
	fmt.Fprintf(tw, "gvks\t%d\n", r.Profile.GVKs)
	fmt.Fprintf(tw, "namespaces\t%d\n", r.Profile.Namespaces)
	if r.Profile.SelectorNamespaces > 0 {
		fmt.Fprintf(tw, "selector_ns\t%d\n", r.Profile.SelectorNamespaces)
	}
	fmt.Fprintf(tw, "watched_gvks\t%.0f\n", r.Measurements.WatchedGVKs)
	fmt.Fprintf(tw, "controller_heap_mb\t%.1f\n", r.Measurements.ControllerHeapMB)
	fmt.Fprintf(tw, "reconcile_p50_ms\t%.2f\n", r.Measurements.ReconcileP50Ms)
	fmt.Fprintf(tw, "reconcile_p95_ms\t%.2f\n", r.Measurements.ReconcileP95Ms)
	fmt.Fprintf(tw, "reconcile_p99_ms\t%.2f\n", r.Measurements.ReconcileP99Ms)
	if r.Profile.SelectorNamespaces > 0 {
		fmt.Fprintf(tw, "e2e_first_ns_p50\t%s\n", r.Measurements.E2EFirstNsP50)
		fmt.Fprintf(tw, "e2e_first_ns_p95\t%s\n", r.Measurements.E2EFirstNsP95)
		fmt.Fprintf(tw, "e2e_first_ns_p99\t%s\n", r.Measurements.E2EFirstNsP99)
		fmt.Fprintf(tw, "e2e_last_ns_p50\t%s\n", r.Measurements.E2ELastNsP50)
		fmt.Fprintf(tw, "e2e_last_ns_p95\t%s\n", r.Measurements.E2ELastNsP95)
		fmt.Fprintf(tw, "e2e_last_ns_p99\t%s\n", r.Measurements.E2ELastNsP99)
	} else {
		fmt.Fprintf(tw, "e2e_p50\t%s\n", r.Measurements.E2EP50)
		fmt.Fprintf(tw, "e2e_p95\t%s\n", r.Measurements.E2EP95)
		fmt.Fprintf(tw, "e2e_p99\t%s\n", r.Measurements.E2EP99)
	}
	fmt.Fprintf(tw, "duration_seconds\t%.1f\n", r.DurationSeconds)
	return tw.Flush()
}
