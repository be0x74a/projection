package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

// fanoutStamps is the number of stamps applied per CP-* fan-out measurement
// pass. 30 lands the p99 estimator on the 30th sample (index 29); see
// quantiles().
const fanoutStamps = 30

// runProfile runs bootstrap → measure → teardown for one profile and returns
// the Report. Mixed profiles run all applicable measurement paths and emit
// every distribution that was exercised.
func runProfile(ctx context.Context, c *clients, p Profile, metricsURL string) (*Report, error) {
	start := time.Now()

	res, err := bootstrap(ctx, c, p)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	defer teardown(context.Background(), c, res) // best-effort, even on error

	// Steady-state settle: give the controller time to reconcile the initial
	// backlog before we start measuring.
	settle := 30 * time.Second
	if p.NamespacedProjections > 500 || p.SelectorNamespaces > 500 || p.ListNamespaces > 500 {
		settle = 60 * time.Second
	}
	time.Sleep(settle)

	// Initial scrape (baseline for CPU delta). Tolerate scrape failure: the
	// chart defaults to metrics.secure=true (auth-gated /metrics), and the
	// harness ships without bearer-token wiring — designed for the
	// `make run` plain-HTTP flow. A scrape failure isn't worth aborting the
	// whole profile after a long bootstrap; the latency measurement is the
	// primary signal. CPU/heap/reconcile-time numbers will report as zero.
	baseline, err := scrapeController(ctx, metricsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: baseline scrape failed, controller-side metrics unavailable: %v\n", err)
		baseline = MetricsSnapshot{}
	}

	// Measurement window. Run every applicable shape; mixed profiles get
	// all three populated.
	var m Measurements
	if p.NamespacedProjections > 0 {
		sample := res.NPRefs
		if len(sample) > 100 {
			sample = sample[:100]
		}
		latency, mErr := measureE2ESingle(ctx, c, sample)
		if mErr != nil {
			return nil, fmt.Errorf("measure NP e2e: %w", mErr)
		}
		m.E2ENPSamples = latency.Samples
		m.E2ENPP50, m.E2ENPP95, m.E2ENPP99 = latency.P50, latency.P95, latency.P99
	}
	if p.SelectorNamespaces > 0 {
		fan, mErr := measureE2EClusterFanout(ctx, c, *res.CPSelectorRef, res.CPSelectorDsts, fanoutStamps)
		if mErr != nil {
			return nil, fmt.Errorf("measure cp-selector e2e: %w", mErr)
		}
		m.E2ECPSelSamples = fan.Samples
		m.E2ECPSelEarliestP50, m.E2ECPSelEarliestP95, m.E2ECPSelEarliestP99 = fan.Earliest.P50, fan.Earliest.P95, fan.Earliest.P99
		m.E2ECPSelSlowestP50, m.E2ECPSelSlowestP95, m.E2ECPSelSlowestP99 = fan.Slowest.P50, fan.Slowest.P95, fan.Slowest.P99
	}
	if p.ListNamespaces > 0 {
		fan, mErr := measureE2EClusterFanout(ctx, c, *res.CPListRef, res.CPListDsts, fanoutStamps)
		if mErr != nil {
			return nil, fmt.Errorf("measure cp-list e2e: %w", mErr)
		}
		m.E2ECPListSamples = fan.Samples
		m.E2ECPListEarliestP50, m.E2ECPListEarliestP95, m.E2ECPListEarliestP99 = fan.Earliest.P50, fan.Earliest.P95, fan.Earliest.P99
		m.E2ECPListSlowestP50, m.E2ECPListSlowestP95, m.E2ECPListSlowestP99 = fan.Slowest.P50, fan.Slowest.P95, fan.Slowest.P99
	}

	// Final scrape. Same tolerance rationale as the baseline above.
	final, err := scrapeController(ctx, metricsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: final scrape failed, controller-side metrics unavailable: %v\n", err)
		final = MetricsSnapshot{}
	}
	m.WatchedGVKs = final.WatchedGVKs
	m.ControllerHeapMB = final.HeapInuseBytes / 1024 / 1024
	m.ControllerRSSMB = final.RSSBytes / 1024 / 1024
	m.ControllerCPUDelta = final.CPUSecondsTotal - baseline.CPUSecondsTotal
	m.ReconcileP50Ms = final.ReconcileP50 * 1000
	m.ReconcileP95Ms = final.ReconcileP95 * 1000
	m.ReconcileP99Ms = final.ReconcileP99 * 1000

	return &Report{
		Profile: p,
		Environment: Environment{
			KubeconfigHost: c.host,
			Timestamp:      time.Now().UTC().Format(time.RFC3339),
			GoVersion:      runtime.Version(),
			OSArch:         runtime.GOOS + "/" + runtime.GOARCH,
		},
		Measurements:    m,
		DurationSeconds: time.Since(start).Seconds(),
	}, nil
}

// writeReports emits one or more reports in the chosen format to w.
func writeReports(w io.Writer, format string, reports []*Report) error {
	for _, r := range reports {
		switch format {
		case "json":
			if err := r.WriteJSON(w); err != nil {
				return err
			}
		case "text":
			_, _ = fmt.Fprintf(w, "\n=== %s ===\n", r.Profile.Name)
			if err := r.WriteText(w); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown output format %q", format)
		}
	}
	return nil
}
