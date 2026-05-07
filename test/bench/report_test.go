package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReportJSON(t *testing.T) {
	r := Report{
		Profile: Profile{Name: "np-typical", NamespacedProjections: 100, GVKs: 10, Namespaces: 10},
		Environment: Environment{
			KubeconfigHost: "https://127.0.0.1:6443",
			Timestamp:      "2026-04-18T12:00:00Z",
		},
		Measurements: Measurements{
			WatchedGVKs:        10,
			ControllerHeapMB:   42.0,
			ControllerCPUDelta: 5.4,
			ReconcileP50Ms:     6.3,
			ReconcileP95Ms:     18.1,
			ReconcileP99Ms:     27.0,
			E2ENPSamples:       100,
			E2ENPP50:           50 * time.Millisecond,
			E2ENPP95:           180 * time.Millisecond,
			E2ENPP99:           420 * time.Millisecond,
		},
		DurationSeconds: 123.0,
	}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	// Round-trip.
	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Profile.Name != "np-typical" {
		t.Errorf("profile name lost: %+v", back)
	}
	if back.Measurements.E2ENPSamples != 100 {
		t.Errorf("E2ENPSamples lost: %+v", back.Measurements)
	}
}

func TestReportText_NPOnly(t *testing.T) {
	r := Report{
		Profile: Profile{Name: "np-typical", NamespacedProjections: 100, GVKs: 10, Namespaces: 10},
		Measurements: Measurements{
			ReconcileP50Ms: 6.3,
			E2ENPP50:       50 * time.Millisecond,
			E2ENPP95:       180 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"np-typical", "namespaced_projections", "100",
		"e2e_np_p50", "e2e_np_p95", "e2e_np_p99",
		"controller_rss_mb", "controller_cpu_seconds_delta",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	// NP-only profile must NOT print CP-* rows.
	for _, banned := range []string{
		"e2e_cp_sel_earliest", "e2e_cp_sel_slowest",
		"e2e_cp_list_earliest", "e2e_cp_list_slowest",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("NP-only text output unexpectedly includes %q:\n%s", banned, out)
		}
	}
}

func TestReportText_CPSelector(t *testing.T) {
	r := Report{
		Profile: Profile{Name: "cp-selector-typical", SelectorNamespaces: 50, GVKs: 1, Namespaces: 1},
		Measurements: Measurements{
			ReconcileP50Ms:      4.0,
			E2ECPSelEarliestP50: 40 * time.Millisecond,
			E2ECPSelEarliestP99: 120 * time.Millisecond,
			E2ECPSelSlowestP50:  400 * time.Millisecond,
			E2ECPSelSlowestP99:  950 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"selector_namespaces", "50",
		"e2e_cp_sel_earliest_p50", "e2e_cp_sel_slowest_p99",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cp-selector text output missing %q:\n%s", want, out)
		}
	}
	// CP-selector-only must NOT print NP or CP-list rows.
	for _, banned := range []string{
		"e2e_np_p50", "e2e_np_p95",
		"e2e_cp_list_earliest", "e2e_cp_list_slowest",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("cp-selector text output unexpectedly includes %q:\n%s", banned, out)
		}
	}
	// Legacy field names must be gone.
	for _, banned := range []string{"e2e_first_ns", "e2e_last_ns", "e2e_earliest_p50", "e2e_slowest_p50"} {
		if strings.Contains(out, banned) {
			t.Errorf("cp-selector text output unexpectedly includes legacy field %q:\n%s", banned, out)
		}
	}
}

func TestReportText_CPList(t *testing.T) {
	r := Report{
		Profile: Profile{Name: "cp-list-typical", ListNamespaces: 10, GVKs: 1, Namespaces: 1},
		Measurements: Measurements{
			ReconcileP50Ms:       3.0,
			E2ECPListEarliestP50: 25 * time.Millisecond,
			E2ECPListSlowestP99:  300 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"list_namespaces", "10",
		"e2e_cp_list_earliest_p50", "e2e_cp_list_slowest_p99",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cp-list text output missing %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{
		"e2e_np_p50",
		"e2e_cp_sel_earliest", "e2e_cp_sel_slowest",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("cp-list text output unexpectedly includes %q:\n%s", banned, out)
		}
	}
}

func TestReportText_Mixed(t *testing.T) {
	r := Report{
		Profile: Profile{
			Name: "mixed-typical", NamespacedProjections: 100,
			SelectorNamespaces: 50, ListNamespaces: 10, GVKs: 10, Namespaces: 10,
		},
		Measurements: Measurements{
			ReconcileP50Ms:       5.0,
			E2ENPP50:             40 * time.Millisecond,
			E2ECPSelEarliestP50:  35 * time.Millisecond,
			E2ECPSelSlowestP50:   250 * time.Millisecond,
			E2ECPListEarliestP50: 20 * time.Millisecond,
			E2ECPListSlowestP50:  120 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"mixed-typical",
		"e2e_np_p50",
		"e2e_cp_sel_earliest_p50", "e2e_cp_sel_slowest_p50",
		"e2e_cp_list_earliest_p50", "e2e_cp_list_slowest_p50",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mixed text output missing %q:\n%s", want, out)
		}
	}
}
