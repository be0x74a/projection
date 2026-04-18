package main

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"time"
)

// runProfile runs bootstrap → measure → teardown for one profile and returns
// the Report.
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
	if p.Projections > 500 {
		settle = 60 * time.Second
	}
	time.Sleep(settle)

	// Initial scrape (baseline for CPU delta).
	baseline, err := scrapeController(ctx, metricsURL)
	if err != nil {
		return nil, fmt.Errorf("baseline scrape: %w", err)
	}

	// Measurement window. Selector profiles measure first-ns vs last-ns
	// latency on a single source; other profiles sample across Projections.
	var m Measurements
	if p.SelectorNamespaces > 0 {
		const selectorStamps = 30
		sel, err := measureE2ESelector(ctx, c, res.Projections[0],
			res.DestNsList[0], res.DestNsList[len(res.DestNsList)-1], selectorStamps)
		if err != nil {
			return nil, fmt.Errorf("measure selector e2e: %w", err)
		}
		m.E2EFirstNsSamples = sel.Samples
		m.E2EFirstNsP50, m.E2EFirstNsP95, m.E2EFirstNsP99 = sel.First.P50, sel.First.P95, sel.First.P99
		m.E2ELastNsP50, m.E2ELastNsP95, m.E2ELastNsP99 = sel.Last.P50, sel.Last.P95, sel.Last.P99
	} else {
		sample := res.Projections
		if len(sample) > 100 {
			sample = sample[:100]
		}
		latency, err := measureE2ESingle(ctx, c, sample)
		if err != nil {
			return nil, fmt.Errorf("measure e2e: %w", err)
		}
		m.E2ESamples = latency.Samples
		m.E2EP50, m.E2EP95, m.E2EP99 = latency.P50, latency.P95, latency.P99
	}

	// Final scrape.
	final, err := scrapeController(ctx, metricsURL)
	if err != nil {
		return nil, fmt.Errorf("final scrape: %w", err)
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
			fmt.Fprintf(w, "\n=== %s ===\n", r.Profile.Name)
			if err := r.WriteText(w); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown output format %q", format)
		}
	}
	return nil
}
