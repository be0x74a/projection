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
			WatchedGVKs:              10,
			ControllerHeapMB:         42.0,
			ControllerCPUDelta:       5.4,
			ReconcileP50Ms:           6.3,
			ReconcileP95Ms:           18.1,
			ReconcileP99Ms:           27.0,
			E2ENPSourceUpdateSamples: 100,
			E2ENPSourceUpdateP50:     50 * time.Millisecond,
			E2ENPSourceUpdateP95:     180 * time.Millisecond,
			E2ENPSourceUpdateP99:     420 * time.Millisecond,
			E2ENPSelfHealSamples:     100,
			E2ENPSelfHealP50:         70 * time.Millisecond,
			E2ENPSelfHealP95:         220 * time.Millisecond,
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
	if back.Measurements.E2ENPSourceUpdateSamples != 100 {
		t.Errorf("E2ENPSourceUpdateSamples lost: %+v", back.Measurements)
	}
	if back.Measurements.E2ENPSelfHealP50 != 70*time.Millisecond {
		t.Errorf("E2ENPSelfHealP50 lost: %+v", back.Measurements)
	}
	// Verify the renamed JSON tags actually flowed through (catches a typo
	// where the field renames but the json tag stays old).
	raw := buf.String()
	for _, want := range []string{
		`"e2e_np_source_update_p50_ns"`,
		`"e2e_np_self_heal_p50_ns"`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("JSON missing renamed tag %q:\n%s", want, raw)
		}
	}
	for _, banned := range []string{
		`"e2e_np_p50_ns"`,
		`"e2e_cp_sel_earliest_p50_ns"`,
		`"e2e_cp_list_slowest_p99_ns"`,
	} {
		if strings.Contains(raw, banned) {
			t.Errorf("JSON unexpectedly includes legacy tag %q:\n%s", banned, raw)
		}
	}
}

func TestReportText_NPOnly(t *testing.T) {
	r := Report{
		Profile: Profile{Name: "np-typical", NamespacedProjections: 100, GVKs: 10, Namespaces: 10},
		Measurements: Measurements{
			ReconcileP50Ms:       6.3,
			E2ENPSourceUpdateP50: 50 * time.Millisecond,
			E2ENPSourceUpdateP95: 180 * time.Millisecond,
			E2ENPSelfHealSamples: 100,
			E2ENPSelfHealP50:     70 * time.Millisecond,
			E2ENPSelfHealP95:     200 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"np-typical", "namespaced_projections", "100",
		"e2e_np_source_update_p50", "e2e_np_source_update_p95", "e2e_np_source_update_p99",
		"e2e_np_self_heal_p50", "e2e_np_self_heal_p95", "e2e_np_self_heal_p99",
		"controller_rss_mb", "controller_cpu_seconds_delta",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	// NP-only profile must NOT print CP-* rows.
	for _, banned := range []string{
		"e2e_cp_sel_source_update_earliest", "e2e_cp_sel_source_update_slowest",
		"e2e_cp_list_source_update_earliest", "e2e_cp_list_source_update_slowest",
		"e2e_cp_sel_self_heal", "e2e_cp_sel_ns_flip",
		"e2e_cp_list_self_heal",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("NP-only text output unexpectedly includes %q:\n%s", banned, out)
		}
	}
	// Legacy unlabeled-event names must be gone.
	for _, banned := range []string{"e2e_np_p50\t", "e2e_np_p95\t", "e2e_np_p99\t"} {
		if strings.Contains(out, banned) {
			t.Errorf("NP-only text output unexpectedly includes legacy field %q:\n%s", banned, out)
		}
	}
}

func TestReportText_NPOnly_NoSelfHealRowsWhenSamplesZero(t *testing.T) {
	// When NamespacedProjections is set but the harness skipped self-heal
	// (e.g. measurement aborted), the source-update rows still print but the
	// self-heal rows must not.
	r := Report{
		Profile: Profile{Name: "np-typical", NamespacedProjections: 100, GVKs: 10, Namespaces: 10},
		Measurements: Measurements{
			E2ENPSourceUpdateP50: 50 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "e2e_np_source_update_p50") {
		t.Errorf("text output missing source-update row:\n%s", out)
	}
	if strings.Contains(out, "e2e_np_self_heal") {
		t.Errorf("text output unexpectedly includes self-heal row when samples=0:\n%s", out)
	}
}

func TestReportText_CPSelector(t *testing.T) {
	r := Report{
		Profile: Profile{Name: "cp-selector-typical", SelectorNamespaces: 50, GVKs: 1, Namespaces: 1},
		Measurements: Measurements{
			ReconcileP50Ms:                  4.0,
			E2ECPSelSourceUpdateEarliestP50: 40 * time.Millisecond,
			E2ECPSelSourceUpdateEarliestP99: 120 * time.Millisecond,
			E2ECPSelSourceUpdateSlowestP50:  400 * time.Millisecond,
			E2ECPSelSourceUpdateSlowestP99:  950 * time.Millisecond,
			E2ECPSelSelfHealSamples:         20,
			E2ECPSelSelfHealP50:             80 * time.Millisecond,
			E2ECPSelNSFlipCleanupSamples:    20,
			E2ECPSelNSFlipCleanupP50:        90 * time.Millisecond,
			E2ECPSelNSFlipAddSamples:        20,
			E2ECPSelNSFlipAddP50:            120 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"selector_namespaces", "50",
		"e2e_cp_sel_source_update_earliest_p50", "e2e_cp_sel_source_update_slowest_p99",
		"e2e_cp_sel_self_heal_p50",
		"e2e_cp_sel_ns_flip_cleanup_p50",
		"e2e_cp_sel_ns_flip_add_p50",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cp-selector text output missing %q:\n%s", want, out)
		}
	}
	// CP-selector-only must NOT print NP or CP-list rows.
	for _, banned := range []string{
		"e2e_np_source_update_p50", "e2e_np_source_update_p95",
		"e2e_np_self_heal",
		"e2e_cp_list_source_update_earliest", "e2e_cp_list_source_update_slowest",
		"e2e_cp_list_self_heal",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("cp-selector text output unexpectedly includes %q:\n%s", banned, out)
		}
	}
	// Legacy unlabeled field names must be gone.
	for _, banned := range []string{
		"e2e_first_ns", "e2e_last_ns",
		"e2e_earliest_p50", "e2e_slowest_p50",
		"e2e_cp_sel_earliest_p50\t", "e2e_cp_sel_slowest_p50\t",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("cp-selector text output unexpectedly includes legacy field %q:\n%s", banned, out)
		}
	}
}

func TestReportText_CPSelector_SkipNSFlipWhenSamplesZero(t *testing.T) {
	// SelectorNamespaces=1 means K=0 for ns-flip (it's len/2). The harness
	// then leaves the ns-flip fields zero, and WriteText must not emit ns-
	// flip rows.
	r := Report{
		Profile: Profile{Name: "cp-selector-tiny", SelectorNamespaces: 1, GVKs: 1, Namespaces: 1},
		Measurements: Measurements{
			E2ECPSelSourceUpdateEarliestP50: 40 * time.Millisecond,
			E2ECPSelSelfHealSamples:         1,
			E2ECPSelSelfHealP50:             80 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "e2e_cp_sel_self_heal_p50") {
		t.Errorf("text output missing self-heal row:\n%s", out)
	}
	if strings.Contains(out, "e2e_cp_sel_ns_flip") {
		t.Errorf("text output unexpectedly includes ns-flip row when samples=0:\n%s", out)
	}
}

func TestReportText_CPList(t *testing.T) {
	r := Report{
		Profile: Profile{Name: "cp-list-typical", ListNamespaces: 10, GVKs: 1, Namespaces: 1},
		Measurements: Measurements{
			ReconcileP50Ms:                   3.0,
			E2ECPListSourceUpdateEarliestP50: 25 * time.Millisecond,
			E2ECPListSourceUpdateSlowestP99:  300 * time.Millisecond,
			E2ECPListSelfHealSamples:         10,
			E2ECPListSelfHealP50:             60 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"list_namespaces", "10",
		"e2e_cp_list_source_update_earliest_p50", "e2e_cp_list_source_update_slowest_p99",
		"e2e_cp_list_self_heal_p50",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("cp-list text output missing %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{
		"e2e_np_source_update_p50",
		"e2e_cp_sel_source_update_earliest", "e2e_cp_sel_source_update_slowest",
		"e2e_cp_list_ns_flip", "e2e_cp_sel_ns_flip",
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
			ReconcileP50Ms:                   5.0,
			E2ENPSourceUpdateP50:             40 * time.Millisecond,
			E2ENPSelfHealSamples:             100,
			E2ENPSelfHealP50:                 60 * time.Millisecond,
			E2ECPSelSourceUpdateEarliestP50:  35 * time.Millisecond,
			E2ECPSelSourceUpdateSlowestP50:   250 * time.Millisecond,
			E2ECPSelSelfHealSamples:          20,
			E2ECPSelSelfHealP50:              80 * time.Millisecond,
			E2ECPSelNSFlipCleanupSamples:     20,
			E2ECPSelNSFlipCleanupP50:         90 * time.Millisecond,
			E2ECPSelNSFlipAddSamples:         20,
			E2ECPSelNSFlipAddP50:             100 * time.Millisecond,
			E2ECPListSourceUpdateEarliestP50: 20 * time.Millisecond,
			E2ECPListSourceUpdateSlowestP50:  120 * time.Millisecond,
			E2ECPListSelfHealSamples:         10,
			E2ECPListSelfHealP50:             60 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"mixed-typical",
		"e2e_np_source_update_p50",
		"e2e_np_self_heal_p50",
		"e2e_cp_sel_source_update_earliest_p50", "e2e_cp_sel_source_update_slowest_p50",
		"e2e_cp_sel_self_heal_p50",
		"e2e_cp_sel_ns_flip_cleanup_p50",
		"e2e_cp_sel_ns_flip_add_p50",
		"e2e_cp_list_source_update_earliest_p50", "e2e_cp_list_source_update_slowest_p50",
		"e2e_cp_list_self_heal_p50",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mixed text output missing %q:\n%s", want, out)
		}
	}
}
