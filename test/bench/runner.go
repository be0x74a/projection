package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

// fanoutStamps is the number of stamps applied per CP-* fan-out source-update
// measurement pass. 30 lands the p99 estimator on the 30th sample (index 29);
// see quantiles().
const fanoutStamps = 30

// npSourceUpdateMaxSamples / npSelfHealMaxSamples cap the NP-shape per-event
// measurement work at K = min(this, len(NPRefs)). 100 keeps measurement wall
// bounded for stress profiles without losing distribution shape.
const (
	npSourceUpdateMaxSamples = 100
	npSelfHealMaxSamples     = 100
)

// cpFanoutSelfHealMaxSamples / cpFanoutNSFlipMaxSamples cap the CP fan-out
// self-heal and ns-flip event sample sizes. K = min(this, len(dstNs)/2 for
// ns-flip, this for self-heal). The /2 floor for ns-flip avoids flipping
// every destination off and on in one pass — leaving half the fan-out alone
// keeps the controller's other reconcile work as a backdrop, which mimics
// real-world steady state.
const (
	cpFanoutSelfHealMaxSamples = 20
	cpFanoutNSFlipMaxSamples   = 20
)

// betweenEventsSettle is the pause inserted between event measurements within
// one profile, so the tail of the previous event (controller queue drain,
// cache settling) doesn't leak into the next event's distribution. 5s is
// loose enough for any of the events on a small profile, tight enough to
// keep total wall reasonable for stress runs.
const betweenEventsSettle = 5 * time.Second

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

	// Measurement window. Run every applicable shape and event; mixed
	// profiles get all three topologies populated. Each event is followed by
	// a betweenEventsSettle so the next event starts from a quiet
	// controller.
	var m Measurements
	if p.NamespacedProjections > 0 {
		// NP source-update.
		sample := capSample(res.NPRefs, npSourceUpdateMaxSamples)
		latency, mErr := measureE2ESingle(ctx, c, sample)
		if mErr != nil {
			return nil, fmt.Errorf("measure NP source-update: %w", mErr)
		}
		m.E2ENPSourceUpdateSamples = latency.Samples
		m.E2ENPSourceUpdateP50, m.E2ENPSourceUpdateP95, m.E2ENPSourceUpdateP99 = latency.P50, latency.P95, latency.P99

		// NP self-heal.
		time.Sleep(betweenEventsSettle)
		sh, mErr := measureSelfHealNP(ctx, c, capSample(res.NPRefs, npSelfHealMaxSamples))
		if mErr != nil {
			return nil, fmt.Errorf("measure NP self-heal: %w", mErr)
		}
		m.E2ENPSelfHealSamples = sh.Samples
		m.E2ENPSelfHealP50, m.E2ENPSelfHealP95, m.E2ENPSelfHealP99 = sh.P50, sh.P95, sh.P99
	}
	if p.SelectorNamespaces > 0 {
		// CP-selector source-update.
		if m.E2ENPSourceUpdateSamples > 0 || m.E2ENPSelfHealSamples > 0 {
			time.Sleep(betweenEventsSettle)
		}
		fan, mErr := measureE2EClusterFanout(ctx, c, *res.CPSelectorRef, res.CPSelectorDsts, fanoutStamps)
		if mErr != nil {
			return nil, fmt.Errorf("measure cp-selector source-update: %w", mErr)
		}
		m.E2ECPSelSourceUpdateSamples = fan.Samples
		m.E2ECPSelSourceUpdateEarliestP50, m.E2ECPSelSourceUpdateEarliestP95, m.E2ECPSelSourceUpdateEarliestP99 =
			fan.Earliest.P50, fan.Earliest.P95, fan.Earliest.P99
		m.E2ECPSelSourceUpdateSlowestP50, m.E2ECPSelSourceUpdateSlowestP95, m.E2ECPSelSourceUpdateSlowestP99 =
			fan.Slowest.P50, fan.Slowest.P95, fan.Slowest.P99

		// CP-selector self-heal: K destinations from the front of the set.
		// For SelectorNamespaces=1 this is K=1; otherwise capped at 20.
		time.Sleep(betweenEventsSettle)
		sh, mErr := measureSelfHealClusterFanout(ctx, c, res.CPSelectorRef.GVKIdx,
			res.CPSelectorRef.SrcName, capSample(res.CPSelectorDsts, cpFanoutSelfHealMaxSamples))
		if mErr != nil {
			return nil, fmt.Errorf("measure cp-selector self-heal: %w", mErr)
		}
		m.E2ECPSelSelfHealSamples = sh.Samples
		m.E2ECPSelSelfHealP50, m.E2ECPSelSelfHealP95, m.E2ECPSelSelfHealP99 = sh.P50, sh.P95, sh.P99

		// CP-selector ns-flip: cap at min(20, len(dstSet)/2). The /2 floor
		// keeps the rest of the fanout as a steady backdrop. For tiny
		// fanouts (e.g. 1) we skip the event entirely — there's no
		// independent backdrop to keep stable.
		nsFlipK := len(res.CPSelectorDsts) / 2
		if nsFlipK > cpFanoutNSFlipMaxSamples {
			nsFlipK = cpFanoutNSFlipMaxSamples
		}
		if nsFlipK > 0 {
			time.Sleep(betweenEventsSettle)
			cleanup, add, mErr := measureNSFlip(ctx, c,
				res.CPSelectorRef.GVKIdx, res.CPSelectorRef.SrcName,
				capSample(res.CPSelectorDsts, nsFlipK),
				cpSelectorLabelKey, cpSelectorLabelValue)
			if mErr != nil {
				return nil, fmt.Errorf("measure cp-selector ns-flip: %w", mErr)
			}
			m.E2ECPSelNSFlipCleanupSamples = cleanup.Samples
			m.E2ECPSelNSFlipCleanupP50, m.E2ECPSelNSFlipCleanupP95, m.E2ECPSelNSFlipCleanupP99 =
				cleanup.P50, cleanup.P95, cleanup.P99
			m.E2ECPSelNSFlipAddSamples = add.Samples
			m.E2ECPSelNSFlipAddP50, m.E2ECPSelNSFlipAddP95, m.E2ECPSelNSFlipAddP99 =
				add.P50, add.P95, add.P99
		}
	}
	if p.ListNamespaces > 0 {
		// CP-list source-update.
		if m.E2ENPSourceUpdateSamples > 0 || m.E2ECPSelSourceUpdateSamples > 0 {
			time.Sleep(betweenEventsSettle)
		}
		fan, mErr := measureE2EClusterFanout(ctx, c, *res.CPListRef, res.CPListDsts, fanoutStamps)
		if mErr != nil {
			return nil, fmt.Errorf("measure cp-list source-update: %w", mErr)
		}
		m.E2ECPListSourceUpdateSamples = fan.Samples
		m.E2ECPListSourceUpdateEarliestP50, m.E2ECPListSourceUpdateEarliestP95, m.E2ECPListSourceUpdateEarliestP99 =
			fan.Earliest.P50, fan.Earliest.P95, fan.Earliest.P99
		m.E2ECPListSourceUpdateSlowestP50, m.E2ECPListSourceUpdateSlowestP95, m.E2ECPListSourceUpdateSlowestP99 =
			fan.Slowest.P50, fan.Slowest.P95, fan.Slowest.P99

		// CP-list self-heal. List destinations don't react to namespace
		// label changes, so ns-flip is not exercised on this path.
		time.Sleep(betweenEventsSettle)
		sh, mErr := measureSelfHealClusterFanout(ctx, c, res.CPListRef.GVKIdx,
			res.CPListRef.SrcName, capSample(res.CPListDsts, cpFanoutSelfHealMaxSamples))
		if mErr != nil {
			return nil, fmt.Errorf("measure cp-list self-heal: %w", mErr)
		}
		m.E2ECPListSelfHealSamples = sh.Samples
		m.E2ECPListSelfHealP50, m.E2ECPListSelfHealP95, m.E2ECPListSelfHealP99 = sh.P50, sh.P95, sh.P99
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
