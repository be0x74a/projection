package main

import (
	"context"
	"fmt"
	"io"
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
	defer resp.Body.Close()
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

// SelectorLatencyResult captures the first-listed and last-listed
// destination-namespace latency distributions for a selector profile. The
// spread between First and Last exposes the fan-out cost the selector
// profile exists to measure.
type SelectorLatencyResult struct {
	Samples int
	First   LatencyResult
	Last    LatencyResult
}

// quantiles returns p50/p95/p99 from a sorted duration slice via index lookup.
func quantiles(durations []time.Duration) (p50, p95, p99 time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0
	}
	q := func(frac float64) time.Duration {
		return durations[int(float64(len(durations)-1)*frac)]
	}
	return q(0.50), q(0.95), q(0.99)
}

// stampSource patches a unix-nano timestamp annotation on one source object.
// Returns the stamp string and the time the patch was issued.
func stampSource(ctx context.Context, c *clients, ref projectionRef) (string, time.Time, error) {
	t0 := time.Now()
	stamp := fmt.Sprintf("%d", t0.UnixNano())
	patchBody := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, benchAnnotationStamp, stamp)
	_, err := c.dynamic.Resource(gvr(ref.GVKIdx)).Namespace(ref.SrcNs).
		Patch(ctx, ref.SrcName, k8stypes.MergePatchType, []byte(patchBody), metav1.PatchOptions{})
	return stamp, t0, err
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
// namespace. Returns one latency distribution. Used for non-selector profiles.
func measureE2ESingle(ctx context.Context, c *clients, sample []projectionRef) (LatencyResult, error) {
	var durations []time.Duration
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

// measureE2ESelector stamps the single selector-profile source `stamps`
// times and records the latency to both the first-listed and last-listed
// destination namespaces per stamp. Two independent distributions expose
// the fan-out spread (last-ns p99 > first-ns p99 by the time it takes the
// controller to iterate matching namespaces).
func measureE2ESelector(ctx context.Context, c *clients, src projectionRef, firstDstNs, lastDstNs string, stamps int) (SelectorLatencyResult, error) {
	var firstDur, lastDur []time.Duration
	for i := 0; i < stamps; i++ {
		stamp, t0, err := stampSource(ctx, c, src)
		if err != nil {
			return SelectorLatencyResult{}, fmt.Errorf("patching source %s/%s: %w", src.SrcNs, src.SrcName, err)
		}
		eFirst, err := waitForStamp(ctx, c, src.GVKIdx, firstDstNs, src.SrcName, stamp, t0)
		if err != nil {
			return SelectorLatencyResult{}, fmt.Errorf("first-ns %s: %w", firstDstNs, err)
		}
		eLast, err := waitForStamp(ctx, c, src.GVKIdx, lastDstNs, src.SrcName, stamp, t0)
		if err != nil {
			return SelectorLatencyResult{}, fmt.Errorf("last-ns %s: %w", lastDstNs, err)
		}
		firstDur = append(firstDur, eFirst)
		lastDur = append(lastDur, eLast)
	}
	sort.Slice(firstDur, func(i, j int) bool { return firstDur[i] < firstDur[j] })
	sort.Slice(lastDur, func(i, j int) bool { return lastDur[i] < lastDur[j] })
	f50, f95, f99 := quantiles(firstDur)
	l50, l95, l99 := quantiles(lastDur)
	return SelectorLatencyResult{
		Samples: stamps,
		First:   LatencyResult{Samples: stamps, P50: f50, P95: f95, P99: f99},
		Last:    LatencyResult{Samples: stamps, P50: l50, P95: l95, P99: l99},
	}, nil
}
