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
		Profile: Profile{Name: "small", Projections: 100, GVKs: 10, Namespaces: 10},
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
			E2ESamples:         100,
			E2EP50:             50 * time.Millisecond,
			E2EP95:             180 * time.Millisecond,
			E2EP99:             420 * time.Millisecond,
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
	if back.Profile.Name != "small" {
		t.Errorf("profile name lost: %+v", back)
	}
}

func TestReportText(t *testing.T) {
	r := Report{
		Profile:      Profile{Name: "small", Projections: 100, GVKs: 10, Namespaces: 10},
		Measurements: Measurements{ReconcileP50Ms: 6.3, E2EP95: 180 * time.Millisecond},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"small", "100", "p50", "p95"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	// Non-selector profile must NOT print selector-specific rows.
	for _, banned := range []string{"e2e_first_ns", "e2e_last_ns"} {
		if strings.Contains(out, banned) {
			t.Errorf("text output unexpectedly includes %q for non-selector profile:\n%s", banned, out)
		}
	}
}

func TestReportText_Selector(t *testing.T) {
	r := Report{
		Profile: Profile{Name: "selector", Projections: 1, GVKs: 1, Namespaces: 1, SelectorNamespaces: 100},
		Measurements: Measurements{
			ReconcileP50Ms: 4.0,
			E2EFirstNsP50:  40 * time.Millisecond,
			E2EFirstNsP99:  120 * time.Millisecond,
			E2ELastNsP50:   400 * time.Millisecond,
			E2ELastNsP99:   950 * time.Millisecond,
		},
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"selector_ns", "e2e_first_ns_p50", "e2e_last_ns_p99", "100"} {
		if !strings.Contains(out, want) {
			t.Errorf("selector text output missing %q:\n%s", want, out)
		}
	}
	// Selector profile must NOT print the non-selector e2e_p50/p95/p99 rows.
	for _, banned := range []string{"\ne2e_p50", "\ne2e_p95", "\ne2e_p99"} {
		if strings.Contains(out, banned) {
			t.Errorf("selector text output unexpectedly includes %q:\n%s", banned, out)
		}
	}
}
