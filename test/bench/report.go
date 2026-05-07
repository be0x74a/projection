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

// Measurements is the per-profile result. Up to three e2e distributions
// coexist for mixed profiles: NP single-target latency, CP-selector fan-out
// (earliest/slowest), and CP-list fan-out (earliest/slowest). Zero fields
// indicate the corresponding shape was not exercised.
type Measurements struct {
	WatchedGVKs        float64 `json:"watched_gvks"`
	ControllerHeapMB   float64 `json:"controller_heap_mb"`
	ControllerRSSMB    float64 `json:"controller_rss_mb"`
	ControllerCPUDelta float64 `json:"controller_cpu_seconds_delta"`
	ReconcileP50Ms     float64 `json:"reconcile_p50_ms"`
	ReconcileP95Ms     float64 `json:"reconcile_p95_ms"`
	ReconcileP99Ms     float64 `json:"reconcile_p99_ms"`

	// NP latency (single-target). Zero when no NP shape.
	E2ENPSamples int           `json:"e2e_np_samples,omitempty"`
	E2ENPP50     time.Duration `json:"e2e_np_p50_ns,omitempty"`
	E2ENPP95     time.Duration `json:"e2e_np_p95_ns,omitempty"`
	E2ENPP99     time.Duration `json:"e2e_np_p99_ns,omitempty"`

	// CP-selector fan-out (earliest + slowest). Zero when no CP-selector shape.
	E2ECPSelSamples     int           `json:"e2e_cp_sel_samples,omitempty"`
	E2ECPSelEarliestP50 time.Duration `json:"e2e_cp_sel_earliest_p50_ns,omitempty"`
	E2ECPSelEarliestP95 time.Duration `json:"e2e_cp_sel_earliest_p95_ns,omitempty"`
	E2ECPSelEarliestP99 time.Duration `json:"e2e_cp_sel_earliest_p99_ns,omitempty"`
	E2ECPSelSlowestP50  time.Duration `json:"e2e_cp_sel_slowest_p50_ns,omitempty"`
	E2ECPSelSlowestP95  time.Duration `json:"e2e_cp_sel_slowest_p95_ns,omitempty"`
	E2ECPSelSlowestP99  time.Duration `json:"e2e_cp_sel_slowest_p99_ns,omitempty"`

	// CP-list fan-out (earliest + slowest). Zero when no CP-list shape.
	E2ECPListSamples     int           `json:"e2e_cp_list_samples,omitempty"`
	E2ECPListEarliestP50 time.Duration `json:"e2e_cp_list_earliest_p50_ns,omitempty"`
	E2ECPListEarliestP95 time.Duration `json:"e2e_cp_list_earliest_p95_ns,omitempty"`
	E2ECPListEarliestP99 time.Duration `json:"e2e_cp_list_earliest_p99_ns,omitempty"`
	E2ECPListSlowestP50  time.Duration `json:"e2e_cp_list_slowest_p50_ns,omitempty"`
	E2ECPListSlowestP95  time.Duration `json:"e2e_cp_list_slowest_p95_ns,omitempty"`
	E2ECPListSlowestP99  time.Duration `json:"e2e_cp_list_slowest_p99_ns,omitempty"`
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
	row("namespaced_projections\t%d\n", r.Profile.NamespacedProjections)
	row("selector_namespaces\t%d\n", r.Profile.SelectorNamespaces)
	row("list_namespaces\t%d\n", r.Profile.ListNamespaces)
	row("gvks\t%d\n", r.Profile.GVKs)
	row("namespaces\t%d\n", r.Profile.Namespaces)
	row("watched_gvks\t%.0f\n", r.Measurements.WatchedGVKs)
	row("controller_heap_mb\t%.1f\n", r.Measurements.ControllerHeapMB)
	row("controller_rss_mb\t%.1f\n", r.Measurements.ControllerRSSMB)
	row("controller_cpu_seconds_delta\t%.2f\n", r.Measurements.ControllerCPUDelta)
	row("reconcile_p50_ms\t%.2f\n", r.Measurements.ReconcileP50Ms)
	row("reconcile_p95_ms\t%.2f\n", r.Measurements.ReconcileP95Ms)
	row("reconcile_p99_ms\t%.2f\n", r.Measurements.ReconcileP99Ms)
	if r.Profile.NamespacedProjections > 0 {
		row("e2e_np_p50\t%s\n", r.Measurements.E2ENPP50)
		row("e2e_np_p95\t%s\n", r.Measurements.E2ENPP95)
		row("e2e_np_p99\t%s\n", r.Measurements.E2ENPP99)
	}
	if r.Profile.SelectorNamespaces > 0 {
		row("e2e_cp_sel_earliest_p50\t%s\n", r.Measurements.E2ECPSelEarliestP50)
		row("e2e_cp_sel_earliest_p95\t%s\n", r.Measurements.E2ECPSelEarliestP95)
		row("e2e_cp_sel_earliest_p99\t%s\n", r.Measurements.E2ECPSelEarliestP99)
		row("e2e_cp_sel_slowest_p50\t%s\n", r.Measurements.E2ECPSelSlowestP50)
		row("e2e_cp_sel_slowest_p95\t%s\n", r.Measurements.E2ECPSelSlowestP95)
		row("e2e_cp_sel_slowest_p99\t%s\n", r.Measurements.E2ECPSelSlowestP99)
	}
	if r.Profile.ListNamespaces > 0 {
		row("e2e_cp_list_earliest_p50\t%s\n", r.Measurements.E2ECPListEarliestP50)
		row("e2e_cp_list_earliest_p95\t%s\n", r.Measurements.E2ECPListEarliestP95)
		row("e2e_cp_list_earliest_p99\t%s\n", r.Measurements.E2ECPListEarliestP99)
		row("e2e_cp_list_slowest_p50\t%s\n", r.Measurements.E2ECPListSlowestP50)
		row("e2e_cp_list_slowest_p95\t%s\n", r.Measurements.E2ECPListSlowestP95)
		row("e2e_cp_list_slowest_p99\t%s\n", r.Measurements.E2ECPListSlowestP99)
	}
	row("duration_seconds\t%.1f\n", r.DurationSeconds)
	return tw.Flush()
}
