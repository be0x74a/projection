package main

import (
	"strings"
	"testing"
)

const sampleMetrics = `
# HELP projection_watched_gvks Number of distinct source GroupVersionKinds the controller is currently watching.
# TYPE projection_watched_gvks gauge
projection_watched_gvks 7
# HELP go_memstats_heap_inuse_bytes Bytes in in-use spans.
# TYPE go_memstats_heap_inuse_bytes gauge
go_memstats_heap_inuse_bytes 4.2e+07
# HELP process_cpu_seconds_total Total user and system CPU time spent in seconds.
# TYPE process_cpu_seconds_total counter
process_cpu_seconds_total 12.34
# HELP controller_runtime_reconcile_time_seconds Length of time per reconciliation.
# TYPE controller_runtime_reconcile_time_seconds histogram
controller_runtime_reconcile_time_seconds_bucket{controller="projection",le="0.005"} 100
controller_runtime_reconcile_time_seconds_bucket{controller="projection",le="0.01"} 180
controller_runtime_reconcile_time_seconds_bucket{controller="projection",le="0.025"} 195
controller_runtime_reconcile_time_seconds_bucket{controller="projection",le="0.05"} 199
controller_runtime_reconcile_time_seconds_bucket{controller="projection",le="0.1"} 200
controller_runtime_reconcile_time_seconds_bucket{controller="projection",le="+Inf"} 200
controller_runtime_reconcile_time_seconds_sum{controller="projection"} 1.23
controller_runtime_reconcile_time_seconds_count{controller="projection"} 200
`

func TestParseMetrics(t *testing.T) {
	m, err := parseMetrics(strings.NewReader(sampleMetrics))
	if err != nil {
		t.Fatalf("parseMetrics: %v", err)
	}
	if m.WatchedGVKs != 7 {
		t.Errorf("WatchedGVKs: got %v, want 7", m.WatchedGVKs)
	}
	if m.HeapInuseBytes != 4.2e7 {
		t.Errorf("HeapInuseBytes: got %v, want 4.2e7", m.HeapInuseBytes)
	}
	if m.CPUSecondsTotal != 12.34 {
		t.Errorf("CPUSecondsTotal: got %v, want 12.34", m.CPUSecondsTotal)
	}
	// p50 falls in the 0.005-0.01 bucket (100 < 100 < 180); estimate via linear
	// interpolation. Exact value depends on the estimator; accept anything in
	// [0.005, 0.01].
	if m.ReconcileP50 < 0.005 || m.ReconcileP50 > 0.01 {
		t.Errorf("ReconcileP50: got %v, want in [0.005, 0.01]", m.ReconcileP50)
	}
	if m.ReconcileP99 < 0.025 || m.ReconcileP99 > 0.05 {
		t.Errorf("ReconcileP99: got %v, want in [0.025, 0.05]", m.ReconcileP99)
	}
}
