package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// MetricsSnapshot is the subset of controller-side metrics the harness cares
// about.
type MetricsSnapshot struct {
	WatchedGVKs     float64
	HeapInuseBytes  float64
	RSSBytes        float64
	CPUSecondsTotal float64
	ReconcileP50    float64
	ReconcileP95    float64
	ReconcileP99    float64
}

// parseMetrics parses a Prometheus text-format stream and extracts the
// snapshot. Histogram quantiles are estimated from buckets via linear
// interpolation.
func parseMetrics(r io.Reader) (MetricsSnapshot, error) {
	p := expfmt.NewTextParser(model.UTF8Validation)
	families, err := p.TextToMetricFamilies(r)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	var out MetricsSnapshot

	if mf, ok := families["projection_watched_gvks"]; ok && len(mf.Metric) > 0 {
		out.WatchedGVKs = mf.Metric[0].GetGauge().GetValue()
	}
	if mf, ok := families["go_memstats_heap_inuse_bytes"]; ok && len(mf.Metric) > 0 {
		out.HeapInuseBytes = mf.Metric[0].GetGauge().GetValue()
	}
	if mf, ok := families["process_resident_memory_bytes"]; ok && len(mf.Metric) > 0 {
		out.RSSBytes = mf.Metric[0].GetGauge().GetValue()
	}
	if mf, ok := families["process_cpu_seconds_total"]; ok && len(mf.Metric) > 0 {
		out.CPUSecondsTotal = mf.Metric[0].GetCounter().GetValue()
	}
	if mf, ok := families["controller_runtime_reconcile_time_seconds"]; ok {
		// Find the projection controller.
		for _, m := range mf.Metric {
			if !hasLabel(m, "controller", "projection") {
				continue
			}
			h := m.GetHistogram()
			if h == nil {
				continue
			}
			out.ReconcileP50 = histogramQuantile(h, 0.50)
			out.ReconcileP95 = histogramQuantile(h, 0.95)
			out.ReconcileP99 = histogramQuantile(h, 0.99)
			break
		}
	}
	return out, nil
}

func hasLabel(m *dto.Metric, k, v string) bool {
	for _, lp := range m.Label {
		if lp.GetName() == k && lp.GetValue() == v {
			return true
		}
	}
	return false
}

// histogramQuantile estimates the q-th quantile from a Prometheus histogram
// via linear interpolation within the bucket that contains the target rank.
// Mirrors Prometheus' histogram_quantile() enough for benchmark use.
func histogramQuantile(h *dto.Histogram, q float64) float64 {
	buckets := h.Bucket
	if len(buckets) == 0 {
		return 0
	}
	total := h.GetSampleCount()
	target := float64(total) * q
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].GetUpperBound() < buckets[j].GetUpperBound()
	})
	var prevCount float64
	var prevBound float64
	for _, b := range buckets {
		count := float64(b.GetCumulativeCount())
		if count >= target {
			bound := b.GetUpperBound()
			if bound >= 1e300 { // +Inf bucket
				return prevBound
			}
			// Linear interpolation within [prevBound, bound].
			if count == prevCount {
				return bound
			}
			frac := (target - prevCount) / (count - prevCount)
			return prevBound + frac*(bound-prevBound)
		}
		prevCount = count
		prevBound = b.GetUpperBound()
	}
	return prevBound
}

// scrapeController fetches /metrics from the given URL and parses it.
// Expects the controller-runtime default :8443 (or plain :8080 when running
// via `make run` in a dev shell). Caller resolves the URL.
func scrapeController(ctx context.Context, url string) (MetricsSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return MetricsSnapshot{}, fmt.Errorf("scraping %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return MetricsSnapshot{}, fmt.Errorf("scraping %s: status %d", url, resp.StatusCode)
	}
	return parseMetrics(resp.Body)
}

// LatencyResult captures one source→destination E2E latency distribution.
type LatencyResult struct {
	Samples       int
	P50, P95, P99 time.Duration
}

// FanoutLatencyResult captures the fan-out latency distribution for a
// ClusterProjection (selector or list), split into two paired distributions
// per stamp:
//
//   - Earliest: time from source stamp to the moment the *first* destination
//     in the fan-out set is observed carrying the new stamp.
//   - Slowest:  time from source stamp to the moment the *last*  destination
//     in the fan-out set is observed carrying the new stamp (i.e. all N
//     destinations have caught up).
//
// The spread between Earliest and Slowest is the honest SLI for fan-out:
// users see the tail, not the median.
type FanoutLatencyResult struct {
	Samples  int
	Earliest LatencyResult
	Slowest  LatencyResult
}

// quantiles returns p50/p95/p99 from a sorted duration slice via index lookup.
// Uses the nearest-rank-with-ceiling estimator so a 30-sample p99 lands on the
// 30th item (index 29) rather than truncating to the 29th (index 28).
func quantiles(durations []time.Duration) (p50, p95, p99 time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0
	}
	q := func(frac float64) time.Duration {
		return durations[int(math.Ceil(float64(len(durations)-1)*frac))]
	}
	return q(0.50), q(0.95), q(0.99)
}

// stampSourceAt patches a unix-nano timestamp annotation on one source
// object (gvkIdx, ns, name). Returns the stamp string and the time the
// patch was issued.
func stampSourceAt(ctx context.Context, c *clients, gvkIdx int, srcNs, srcName string) (string, time.Time, error) {
	t0 := time.Now()
	stamp := fmt.Sprintf("%d", t0.UnixNano())
	patchBody := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, benchAnnotationStamp, stamp)
	_, err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(srcNs).
		Patch(ctx, srcName, k8stypes.MergePatchType, []byte(patchBody), metav1.PatchOptions{})
	return stamp, t0, err
}

// stampSource is the projectionRef-friendly wrapper around stampSourceAt.
func stampSource(ctx context.Context, c *clients, ref projectionRef) (string, time.Time, error) {
	return stampSourceAt(ctx, c, ref.GVKIdx, ref.SrcNs, ref.SrcName)
}

// waitForStamp polls one destination namespace for `stamp` on the annotation.
// Returns the elapsed duration from `t0` when the stamp appears, or an error
// on timeout / apiserver error.
func waitForStamp(ctx context.Context, c *clients, gvkIdx int, dstNs, name, stamp string, t0 time.Time) (time.Duration, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timeout waiting for stamp %s on %s/%s", stamp, dstNs, name)
		}
		dst, err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(dstNs).
			Get(ctx, name, metav1.GetOptions{})
		if err == nil && dst.GetAnnotations()[benchAnnotationStamp] == stamp {
			return time.Since(t0), nil
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return 0, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// measureE2ESingle stamps each sample source and polls its single destination
// namespace. Returns one latency distribution. Used for NP profiles.
func measureE2ESingle(ctx context.Context, c *clients, sample []projectionRef) (LatencyResult, error) {
	durations := make([]time.Duration, 0, len(sample))
	for _, ref := range sample {
		stamp, t0, err := stampSource(ctx, c, ref)
		if err != nil {
			return LatencyResult{}, fmt.Errorf("patching source %s/%s: %w", ref.SrcNs, ref.SrcName, err)
		}
		elapsed, err := waitForStamp(ctx, c, ref.GVKIdx, ref.DstNs, ref.SrcName, stamp, t0)
		if err != nil {
			return LatencyResult{}, err
		}
		durations = append(durations, elapsed)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50, p95, p99 := quantiles(durations)
	return LatencyResult{Samples: len(durations), P50: p50, P95: p95, P99: p99}, nil
}

// waitForStampAll polls the destination GVK cluster-wide on a 10ms tick and
// reports, for a single source stamp, two per-stamp durations measured from
// t0:
//
//   - earliest: when the first destination in allDstNs is observed carrying
//     the new stamp on benchAnnotationStamp.
//   - slowest:  when the last  destination in allDstNs is observed carrying
//     the new stamp (i.e. every destination has caught up).
//
// One List call per tick (≈100/sec, well under the harness QPS=500 limit).
// Returns on deadline (30s) with an error listing the namespaces still
// missing the stamp — which surfaces stuck / dropped reconciles instead of
// silently skewing the distribution.
//
// Rationale for the List-driven design: the previous first-ns/last-ns probe
// relied on controller iteration order matching Go map iteration inside the
// cached namespace List. Empirical per-write timestamps showed the two
// pre-chosen namespaces land adjacent to each other near the end of the
// write loop — so the "first/last" pair collected two near-duplicate
// samples of the slowest writes, not the spread. Listing N destinations per
// tick and matching by annotation == stamp removes that dependence on
// iteration order entirely.
func waitForStampAll(
	ctx context.Context,
	c *clients,
	gvkIdx int,
	allDstNs []string,
	name, stamp string,
	t0 time.Time,
) (earliest, slowest time.Duration, err error) {
	// Set-membership filter for "is this list entry one of our destinations".
	wantNs := make(map[string]struct{}, len(allDstNs))
	for _, ns := range allDstNs {
		wantNs[ns] = struct{}{}
	}
	seen := make(map[string]struct{}, len(allDstNs))
	total := len(allDstNs)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			missing := make([]string, 0, total-len(seen))
			for ns := range wantNs {
				if _, ok := seen[ns]; !ok {
					missing = append(missing, ns)
				}
			}
			sort.Strings(missing)
			return 0, 0, fmt.Errorf("timeout waiting for stamp %s on %d/%d destinations (missing: %v)",
				stamp, total-len(seen), total, missing)
		}
		// Cluster-wide list of the bench GVK. The name + wantNs filter
		// discards everything not in our fan-out set; in mixed-* profiles
		// the NP path also writes destinations of GVK index 0 in unrelated
		// namespaces, which arrive in this list and are skipped by the
		// name filter (NP names are src-N, CP fan-out names are the CP
		// source name).
		list, listErr := c.dynamic.Resource(gvr(gvkIdx)).List(ctx, metav1.ListOptions{})
		if listErr != nil && !apierrors.IsNotFound(listErr) {
			return 0, 0, listErr
		}
		if list != nil {
			for i := range list.Items {
				item := &list.Items[i]
				if item.GetName() != name {
					continue
				}
				ns := item.GetNamespace()
				if _, ok := wantNs[ns]; !ok {
					continue
				}
				if _, already := seen[ns]; already {
					continue
				}
				if item.GetAnnotations()[benchAnnotationStamp] != stamp {
					continue
				}
				if len(seen) == 0 {
					earliest = time.Since(t0)
				}
				seen[ns] = struct{}{}
			}
		}
		if len(seen) == total {
			slowest = time.Since(t0)
			return earliest, slowest, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// measureE2EClusterFanout stamps the single ClusterProjection source `stamps`
// times and, for each stamp, records the earliest and slowest per-stamp
// propagation across the full destination set via waitForStampAll. Two paired
// distributions expose the fan-out spread users actually see. Caller passes
// the destination set explicitly, so this function works for both
// CP-selector and CP-list shapes.
func measureE2EClusterFanout(
	ctx context.Context,
	c *clients,
	src clusterProjectionRef,
	allDstNs []string,
	stamps int,
) (FanoutLatencyResult, error) {
	earliestDur := make([]time.Duration, 0, stamps)
	slowestDur := make([]time.Duration, 0, stamps)
	for i := 0; i < stamps; i++ {
		stamp, t0, err := stampSourceAt(ctx, c, src.GVKIdx, src.SrcNs, src.SrcName)
		if err != nil {
			return FanoutLatencyResult{}, fmt.Errorf("patching source %s/%s: %w", src.SrcNs, src.SrcName, err)
		}
		earliest, slowest, err := waitForStampAll(ctx, c, src.GVKIdx, allDstNs, src.SrcName, stamp, t0)
		if err != nil {
			return FanoutLatencyResult{}, err
		}
		earliestDur = append(earliestDur, earliest)
		slowestDur = append(slowestDur, slowest)
	}
	sort.Slice(earliestDur, func(i, j int) bool { return earliestDur[i] < earliestDur[j] })
	sort.Slice(slowestDur, func(i, j int) bool { return slowestDur[i] < slowestDur[j] })
	e50, e95, e99 := quantiles(earliestDur)
	s50, s95, s99 := quantiles(slowestDur)
	return FanoutLatencyResult{
		Samples:  stamps,
		Earliest: LatencyResult{Samples: stamps, P50: e50, P95: e95, P99: e99},
		Slowest:  LatencyResult{Samples: stamps, P50: s50, P95: s95, P99: s99},
	}, nil
}

// capSample returns sample[:min(len(sample), n)] without allocating. The
// caller is responsible for passing a sample they already shuffled or
// otherwise selected; this helper just enforces the cap.
func capSample[T any](sample []T, n int) []T {
	if n <= 0 || len(sample) <= n {
		return sample
	}
	return sample[:n]
}

// waitForRecreate polls one destination object until it is observed with a
// UID different from `oldUID`. NotFound is treated as "still recreating" and
// the loop continues; any other error aborts. Returns the elapsed duration
// from `t0` when the new UID is observed, or an error on 30s timeout.
//
// Self-heal latency end is "destination CR present with new UID", not "spec
// matches source". The controller's create call is the user-visible event;
// follow-up reconciles to align spec are measured by source-update.
func waitForRecreate(
	ctx context.Context,
	c *clients,
	gvkIdx int,
	dstNs, name string,
	oldUID k8stypes.UID,
	t0 time.Time,
) (time.Duration, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timeout waiting for recreation of %s/%s", dstNs, name)
		}
		dst, err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(dstNs).
			Get(ctx, name, metav1.GetOptions{})
		if err == nil && dst.GetUID() != oldUID && dst.GetUID() != "" {
			return time.Since(t0), nil
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return 0, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForDeletion polls one destination object until it returns NotFound.
// Used by ns-flip cleanup: when a namespace's matching label is removed, the
// destination CR in that namespace should be deleted by the controller.
// Returns the elapsed duration from `t0`, or an error on 30s timeout.
func waitForDeletion(
	ctx context.Context,
	c *clients,
	gvkIdx int,
	dstNs, name string,
	t0 time.Time,
) (time.Duration, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timeout waiting for deletion of %s/%s", dstNs, name)
		}
		_, err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(dstNs).
			Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return time.Since(t0), nil
		}
		if err != nil {
			return 0, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForCreation polls one destination object until it returns successfully
// (i.e. the destination CR has been created in `dstNs`). Used by ns-flip add:
// when a namespace's matching label is re-added, the controller should
// re-create the destination CR. Returns the elapsed duration from `t0`, or
// an error on 30s timeout.
func waitForCreation(
	ctx context.Context,
	c *clients,
	gvkIdx int,
	dstNs, name string,
	t0 time.Time,
) (time.Duration, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timeout waiting for creation of %s/%s", dstNs, name)
		}
		_, err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(dstNs).
			Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			return time.Since(t0), nil
		}
		if !apierrors.IsNotFound(err) {
			return 0, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// measureSelfHealNP deletes each sample destination CR and times the
// controller's recreation. Per-destination latency, no fan-out (each NP
// destination's recreation is independent of the others). Returns one
// LatencyResult.
func measureSelfHealNP(ctx context.Context, c *clients, sample []projectionRef) (LatencyResult, error) {
	durations := make([]time.Duration, 0, len(sample))
	for _, ref := range sample {
		// Capture the original UID so we can distinguish "recreated" from
		// "still being recreated" (NotFound) and from "delete didn't take".
		orig, err := c.dynamic.Resource(gvr(ref.GVKIdx)).Namespace(ref.DstNs).
			Get(ctx, ref.SrcName, metav1.GetOptions{})
		if err != nil {
			return LatencyResult{}, fmt.Errorf("reading destination %s/%s: %w", ref.DstNs, ref.SrcName, err)
		}
		oldUID := orig.GetUID()
		t0 := time.Now()
		if err := c.dynamic.Resource(gvr(ref.GVKIdx)).Namespace(ref.DstNs).
			Delete(ctx, ref.SrcName, metav1.DeleteOptions{}); err != nil {
			return LatencyResult{}, fmt.Errorf("deleting destination %s/%s: %w", ref.DstNs, ref.SrcName, err)
		}
		elapsed, err := waitForRecreate(ctx, c, ref.GVKIdx, ref.DstNs, ref.SrcName, oldUID, t0)
		if err != nil {
			return LatencyResult{}, err
		}
		durations = append(durations, elapsed)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50, p95, p99 := quantiles(durations)
	return LatencyResult{Samples: len(durations), P50: p50, P95: p95, P99: p99}, nil
}

// measureSelfHealClusterFanout deletes K destinations from a CP fan-out set
// (one at a time, sequentially) and times each recreation. Sample is the
// pre-selected subset of `dstNs` namespaces — caller chooses K. Each
// deletion's latency is independent of the others (the controller's recreate
// path is the user-visible event), so this returns a single LatencyResult,
// not a fan-out result.
//
// Works for both CP-selector and CP-list shapes — both write the same
// destination object name to a set of namespaces.
func measureSelfHealClusterFanout(
	ctx context.Context,
	c *clients,
	gvkIdx int,
	dstName string,
	sampleDstNs []string,
) (LatencyResult, error) {
	durations := make([]time.Duration, 0, len(sampleDstNs))
	for _, dstNs := range sampleDstNs {
		orig, err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(dstNs).
			Get(ctx, dstName, metav1.GetOptions{})
		if err != nil {
			return LatencyResult{}, fmt.Errorf("reading destination %s/%s: %w", dstNs, dstName, err)
		}
		oldUID := orig.GetUID()
		t0 := time.Now()
		if err := c.dynamic.Resource(gvr(gvkIdx)).Namespace(dstNs).
			Delete(ctx, dstName, metav1.DeleteOptions{}); err != nil {
			return LatencyResult{}, fmt.Errorf("deleting destination %s/%s: %w", dstNs, dstName, err)
		}
		elapsed, err := waitForRecreate(ctx, c, gvkIdx, dstNs, dstName, oldUID, t0)
		if err != nil {
			return LatencyResult{}, err
		}
		durations = append(durations, elapsed)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50, p95, p99 := quantiles(durations)
	return LatencyResult{Samples: len(durations), P50: p50, P95: p95, P99: p99}, nil
}

// patchNamespaceLabel sets a namespace label to `value`, or removes the key
// entirely when `remove` is true. Idempotent.
func patchNamespaceLabel(ctx context.Context, c *clients, ns, key, value string, remove bool) error {
	var patchBody string
	if remove {
		// JSON-merge-patch: setting a key to null removes it.
		patchBody = fmt.Sprintf(`{"metadata":{"labels":{%q:null}}}`, key)
	} else {
		patchBody = fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, value)
	}
	_, err := c.dynamic.Resource(nsGVR).Patch(ctx, ns, k8stypes.MergePatchType,
		[]byte(patchBody), metav1.PatchOptions{})
	return err
}

// measureNSFlip exercises the CP-selector ns-flip event for a sampled subset
// of destination namespaces. For each namespace in `sampleDstNs`, in
// sequence:
//
//  1. Cleanup phase: remove the matching label, time until the destination
//     CR in that namespace returns NotFound.
//  2. Add phase:     re-add the matching label, time until the destination
//     CR is observed again.
//
// Returns two LatencyResults (cleanup, add). One namespace at a time keeps
// the measurement independent of fan-out scheduling — every flip starts from
// a "rest of the world is steady" baseline.
func measureNSFlip(
	ctx context.Context,
	c *clients,
	gvkIdx int,
	dstName string,
	sampleDstNs []string,
	labelKey, labelValue string,
) (cleanup, add LatencyResult, err error) {
	cleanupDur := make([]time.Duration, 0, len(sampleDstNs))
	addDur := make([]time.Duration, 0, len(sampleDstNs))
	for _, dstNs := range sampleDstNs {
		// Cleanup phase: drop the label, wait for destination delete.
		t0 := time.Now()
		if perr := patchNamespaceLabel(ctx, c, dstNs, labelKey, "", true); perr != nil {
			return LatencyResult{}, LatencyResult{},
				fmt.Errorf("removing label from %s: %w", dstNs, perr)
		}
		elapsed, werr := waitForDeletion(ctx, c, gvkIdx, dstNs, dstName, t0)
		if werr != nil {
			return LatencyResult{}, LatencyResult{}, werr
		}
		cleanupDur = append(cleanupDur, elapsed)

		// Add phase: re-add the label, wait for destination create.
		t1 := time.Now()
		if perr := patchNamespaceLabel(ctx, c, dstNs, labelKey, labelValue, false); perr != nil {
			return LatencyResult{}, LatencyResult{},
				fmt.Errorf("re-adding label to %s: %w", dstNs, perr)
		}
		elapsed, werr = waitForCreation(ctx, c, gvkIdx, dstNs, dstName, t1)
		if werr != nil {
			return LatencyResult{}, LatencyResult{}, werr
		}
		addDur = append(addDur, elapsed)
	}
	sort.Slice(cleanupDur, func(i, j int) bool { return cleanupDur[i] < cleanupDur[j] })
	sort.Slice(addDur, func(i, j int) bool { return addDur[i] < addDur[j] })
	c50, c95, c99 := quantiles(cleanupDur)
	a50, a95, a99 := quantiles(addDur)
	return LatencyResult{Samples: len(cleanupDur), P50: c50, P95: c95, P99: c99},
		LatencyResult{Samples: len(addDur), P50: a50, P95: a95, P99: a99},
		nil
}
