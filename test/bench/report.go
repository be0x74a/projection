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

	// Selector profiles only: two paired distributions per stamp, captured
	// by a List-driven poll over the full matched-destination set. Earliest
	// is the time to the first destination catching the new stamp; Slowest
	// is the time to the last destination catching it. The spread exposes
	// fan-out cost. Zero for non-selector profiles.
	E2EFanoutSamples int           `json:"e2e_fanout_samples,omitempty"`
	E2EEarliestP50   time.Duration `json:"e2e_earliest_p50_ns,omitempty"`
	E2EEarliestP95   time.Duration `json:"e2e_earliest_p95_ns,omitempty"`
	E2EEarliestP99   time.Duration `json:"e2e_earliest_p99_ns,omitempty"`
	E2ESlowestP50    time.Duration `json:"e2e_slowest_p50_ns,omitempty"`
	E2ESlowestP95    time.Duration `json:"e2e_slowest_p95_ns,omitempty"`
	E2ESlowestP99    time.Duration `json:"e2e_slowest_p99_ns,omitempty"`
}

func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func (r *Report) WriteText(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	// tabwriter's Write never errors before Flush (it only buffers), so
	// row() deliberately discards the Fprintf return. The one error we care
	// about is surfaced by tw.Flush() at the end.
	row := func(format string, args ...any) {
		_, _ = fmt.Fprintf(tw, format, args...)
	}
	row("profile\t%s\n", r.Profile.Name)
	row("projections\t%d\n", r.Profile.Projections)
	row("gvks\t%d\n", r.Profile.GVKs)
	row("namespaces\t%d\n", r.Profile.Namespaces)
	if r.Profile.SelectorNamespaces > 0 {
		row("selector_ns\t%d\n", r.Profile.SelectorNamespaces)
	}
	row("watched_gvks\t%.0f\n", r.Measurements.WatchedGVKs)
	row("controller_heap_mb\t%.1f\n", r.Measurements.ControllerHeapMB)
	row("controller_rss_mb\t%.1f\n", r.Measurements.ControllerRSSMB)
	row("controller_cpu_seconds_delta\t%.2f\n", r.Measurements.ControllerCPUDelta)
	row("reconcile_p50_ms\t%.2f\n", r.Measurements.ReconcileP50Ms)
	row("reconcile_p95_ms\t%.2f\n", r.Measurements.ReconcileP95Ms)
	row("reconcile_p99_ms\t%.2f\n", r.Measurements.ReconcileP99Ms)
	if r.Profile.SelectorNamespaces > 0 {
		row("e2e_earliest_p50\t%s\n", r.Measurements.E2EEarliestP50)
		row("e2e_earliest_p95\t%s\n", r.Measurements.E2EEarliestP95)
		row("e2e_earliest_p99\t%s\n", r.Measurements.E2EEarliestP99)
		row("e2e_slowest_p50\t%s\n", r.Measurements.E2ESlowestP50)
		row("e2e_slowest_p95\t%s\n", r.Measurements.E2ESlowestP95)
		row("e2e_slowest_p99\t%s\n", r.Measurements.E2ESlowestP99)
	} else {
		row("e2e_p50\t%s\n", r.Measurements.E2EP50)
		row("e2e_p95\t%s\n", r.Measurements.E2EP95)
		row("e2e_p99\t%s\n", r.Measurements.E2EP99)
	}
	row("duration_seconds\t%.1f\n", r.DurationSeconds)
	return tw.Flush()
}
