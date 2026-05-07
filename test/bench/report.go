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

// Measurements is the per-profile result. Up to three e2e topologies coexist
// (NP single-target, CP-selector fan-out, CP-list fan-out) and each topology
// can record up to three event distributions:
//
//   - source-update: timestamp annotation patched on the source propagates to
//     every destination.
//   - self-heal:     a destination CR is deleted and the controller recreates
//     it (per-destination latency, no fan-out earliest/slowest).
//   - ns-flip:       (CP-selector only) a destination namespace's matching
//     label is removed and re-added; cleanup and add latencies are tracked
//     separately.
//
// Zero / missing fields indicate the corresponding shape did not exercise
// that event.
type Measurements struct {
	WatchedGVKs        float64 `json:"watched_gvks"`
	ControllerHeapMB   float64 `json:"controller_heap_mb"`
	ControllerRSSMB    float64 `json:"controller_rss_mb"`
	ControllerCPUDelta float64 `json:"controller_cpu_seconds_delta"`
	ReconcileP50Ms     float64 `json:"reconcile_p50_ms"`
	ReconcileP95Ms     float64 `json:"reconcile_p95_ms"`
	ReconcileP99Ms     float64 `json:"reconcile_p99_ms"`

	// NP source-update latency (single-target). Zero when no NP shape.
	E2ENPSourceUpdateSamples int           `json:"e2e_np_source_update_samples,omitempty"`
	E2ENPSourceUpdateP50     time.Duration `json:"e2e_np_source_update_p50_ns,omitempty"`
	E2ENPSourceUpdateP95     time.Duration `json:"e2e_np_source_update_p95_ns,omitempty"`
	E2ENPSourceUpdateP99     time.Duration `json:"e2e_np_source_update_p99_ns,omitempty"`

	// NP self-heal latency. Per-destination (no fan-out).
	E2ENPSelfHealSamples int           `json:"e2e_np_self_heal_samples,omitempty"`
	E2ENPSelfHealP50     time.Duration `json:"e2e_np_self_heal_p50_ns,omitempty"`
	E2ENPSelfHealP95     time.Duration `json:"e2e_np_self_heal_p95_ns,omitempty"`
	E2ENPSelfHealP99     time.Duration `json:"e2e_np_self_heal_p99_ns,omitempty"`

	// CP-selector source-update fan-out (earliest + slowest). Zero when no
	// CP-selector shape.
	E2ECPSelSourceUpdateSamples     int           `json:"e2e_cp_sel_source_update_samples,omitempty"`
	E2ECPSelSourceUpdateEarliestP50 time.Duration `json:"e2e_cp_sel_source_update_earliest_p50_ns,omitempty"`
	E2ECPSelSourceUpdateEarliestP95 time.Duration `json:"e2e_cp_sel_source_update_earliest_p95_ns,omitempty"`
	E2ECPSelSourceUpdateEarliestP99 time.Duration `json:"e2e_cp_sel_source_update_earliest_p99_ns,omitempty"`
	E2ECPSelSourceUpdateSlowestP50  time.Duration `json:"e2e_cp_sel_source_update_slowest_p50_ns,omitempty"`
	E2ECPSelSourceUpdateSlowestP95  time.Duration `json:"e2e_cp_sel_source_update_slowest_p95_ns,omitempty"`
	E2ECPSelSourceUpdateSlowestP99  time.Duration `json:"e2e_cp_sel_source_update_slowest_p99_ns,omitempty"`

	// CP-selector self-heal latency (per-destination).
	E2ECPSelSelfHealSamples int           `json:"e2e_cp_sel_self_heal_samples,omitempty"`
	E2ECPSelSelfHealP50     time.Duration `json:"e2e_cp_sel_self_heal_p50_ns,omitempty"`
	E2ECPSelSelfHealP95     time.Duration `json:"e2e_cp_sel_self_heal_p95_ns,omitempty"`
	E2ECPSelSelfHealP99     time.Duration `json:"e2e_cp_sel_self_heal_p99_ns,omitempty"`

	// CP-selector ns-flip cleanup latency: namespace label removed → matching
	// destination CR deleted by the controller.
	E2ECPSelNSFlipCleanupSamples int           `json:"e2e_cp_sel_ns_flip_cleanup_samples,omitempty"`
	E2ECPSelNSFlipCleanupP50     time.Duration `json:"e2e_cp_sel_ns_flip_cleanup_p50_ns,omitempty"`
	E2ECPSelNSFlipCleanupP95     time.Duration `json:"e2e_cp_sel_ns_flip_cleanup_p95_ns,omitempty"`
	E2ECPSelNSFlipCleanupP99     time.Duration `json:"e2e_cp_sel_ns_flip_cleanup_p99_ns,omitempty"`

	// CP-selector ns-flip add latency: namespace label re-added → destination
	// CR recreated.
	E2ECPSelNSFlipAddSamples int           `json:"e2e_cp_sel_ns_flip_add_samples,omitempty"`
	E2ECPSelNSFlipAddP50     time.Duration `json:"e2e_cp_sel_ns_flip_add_p50_ns,omitempty"`
	E2ECPSelNSFlipAddP95     time.Duration `json:"e2e_cp_sel_ns_flip_add_p95_ns,omitempty"`
	E2ECPSelNSFlipAddP99     time.Duration `json:"e2e_cp_sel_ns_flip_add_p99_ns,omitempty"`

	// CP-list source-update fan-out (earliest + slowest). Zero when no
	// CP-list shape.
	E2ECPListSourceUpdateSamples     int           `json:"e2e_cp_list_source_update_samples,omitempty"`
	E2ECPListSourceUpdateEarliestP50 time.Duration `json:"e2e_cp_list_source_update_earliest_p50_ns,omitempty"`
	E2ECPListSourceUpdateEarliestP95 time.Duration `json:"e2e_cp_list_source_update_earliest_p95_ns,omitempty"`
	E2ECPListSourceUpdateEarliestP99 time.Duration `json:"e2e_cp_list_source_update_earliest_p99_ns,omitempty"`
	E2ECPListSourceUpdateSlowestP50  time.Duration `json:"e2e_cp_list_source_update_slowest_p50_ns,omitempty"`
	E2ECPListSourceUpdateSlowestP95  time.Duration `json:"e2e_cp_list_source_update_slowest_p95_ns,omitempty"`
	E2ECPListSourceUpdateSlowestP99  time.Duration `json:"e2e_cp_list_source_update_slowest_p99_ns,omitempty"`

	// CP-list self-heal latency (per-destination).
	E2ECPListSelfHealSamples int           `json:"e2e_cp_list_self_heal_samples,omitempty"`
	E2ECPListSelfHealP50     time.Duration `json:"e2e_cp_list_self_heal_p50_ns,omitempty"`
	E2ECPListSelfHealP95     time.Duration `json:"e2e_cp_list_self_heal_p95_ns,omitempty"`
	E2ECPListSelfHealP99     time.Duration `json:"e2e_cp_list_self_heal_p99_ns,omitempty"`
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
		row("e2e_np_source_update_p50\t%s\n", r.Measurements.E2ENPSourceUpdateP50)
		row("e2e_np_source_update_p95\t%s\n", r.Measurements.E2ENPSourceUpdateP95)
		row("e2e_np_source_update_p99\t%s\n", r.Measurements.E2ENPSourceUpdateP99)
		if r.Measurements.E2ENPSelfHealSamples > 0 {
			row("e2e_np_self_heal_p50\t%s\n", r.Measurements.E2ENPSelfHealP50)
			row("e2e_np_self_heal_p95\t%s\n", r.Measurements.E2ENPSelfHealP95)
			row("e2e_np_self_heal_p99\t%s\n", r.Measurements.E2ENPSelfHealP99)
		}
	}
	if r.Profile.SelectorNamespaces > 0 {
		row("e2e_cp_sel_source_update_earliest_p50\t%s\n", r.Measurements.E2ECPSelSourceUpdateEarliestP50)
		row("e2e_cp_sel_source_update_earliest_p95\t%s\n", r.Measurements.E2ECPSelSourceUpdateEarliestP95)
		row("e2e_cp_sel_source_update_earliest_p99\t%s\n", r.Measurements.E2ECPSelSourceUpdateEarliestP99)
		row("e2e_cp_sel_source_update_slowest_p50\t%s\n", r.Measurements.E2ECPSelSourceUpdateSlowestP50)
		row("e2e_cp_sel_source_update_slowest_p95\t%s\n", r.Measurements.E2ECPSelSourceUpdateSlowestP95)
		row("e2e_cp_sel_source_update_slowest_p99\t%s\n", r.Measurements.E2ECPSelSourceUpdateSlowestP99)
		if r.Measurements.E2ECPSelSelfHealSamples > 0 {
			row("e2e_cp_sel_self_heal_p50\t%s\n", r.Measurements.E2ECPSelSelfHealP50)
			row("e2e_cp_sel_self_heal_p95\t%s\n", r.Measurements.E2ECPSelSelfHealP95)
			row("e2e_cp_sel_self_heal_p99\t%s\n", r.Measurements.E2ECPSelSelfHealP99)
		}
		if r.Measurements.E2ECPSelNSFlipCleanupSamples > 0 {
			row("e2e_cp_sel_ns_flip_cleanup_p50\t%s\n", r.Measurements.E2ECPSelNSFlipCleanupP50)
			row("e2e_cp_sel_ns_flip_cleanup_p95\t%s\n", r.Measurements.E2ECPSelNSFlipCleanupP95)
			row("e2e_cp_sel_ns_flip_cleanup_p99\t%s\n", r.Measurements.E2ECPSelNSFlipCleanupP99)
		}
		if r.Measurements.E2ECPSelNSFlipAddSamples > 0 {
			row("e2e_cp_sel_ns_flip_add_p50\t%s\n", r.Measurements.E2ECPSelNSFlipAddP50)
			row("e2e_cp_sel_ns_flip_add_p95\t%s\n", r.Measurements.E2ECPSelNSFlipAddP95)
			row("e2e_cp_sel_ns_flip_add_p99\t%s\n", r.Measurements.E2ECPSelNSFlipAddP99)
		}
	}
	if r.Profile.ListNamespaces > 0 {
		row("e2e_cp_list_source_update_earliest_p50\t%s\n", r.Measurements.E2ECPListSourceUpdateEarliestP50)
		row("e2e_cp_list_source_update_earliest_p95\t%s\n", r.Measurements.E2ECPListSourceUpdateEarliestP95)
		row("e2e_cp_list_source_update_earliest_p99\t%s\n", r.Measurements.E2ECPListSourceUpdateEarliestP99)
		row("e2e_cp_list_source_update_slowest_p50\t%s\n", r.Measurements.E2ECPListSourceUpdateSlowestP50)
		row("e2e_cp_list_source_update_slowest_p95\t%s\n", r.Measurements.E2ECPListSourceUpdateSlowestP95)
		row("e2e_cp_list_source_update_slowest_p99\t%s\n", r.Measurements.E2ECPListSourceUpdateSlowestP99)
		if r.Measurements.E2ECPListSelfHealSamples > 0 {
			row("e2e_cp_list_self_heal_p50\t%s\n", r.Measurements.E2ECPListSelfHealP50)
			row("e2e_cp_list_self_heal_p95\t%s\n", r.Measurements.E2ECPListSelfHealP95)
			row("e2e_cp_list_self_heal_p99\t%s\n", r.Measurements.E2ECPListSelfHealP99)
		}
	}
	row("duration_seconds\t%.1f\n", r.DurationSeconds)
	return tw.Flush()
}
