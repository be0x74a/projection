/*
Copyright 2026 The projection Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Result label values for reconcileTotal.
const (
	resultSuccess          = "success"
	resultConflict         = "conflict"
	resultSourceError      = "source_error"
	resultDestinationError = "destination_error"
)

// Kind label values for reconcileTotal. The strings match the CRD Kind names
// verbatim because they are externally visible — dashboards and PromQL
// expressions filter on them.
const (
	kindProjection        = "Projection"
	kindClusterProjection = "ClusterProjection"
)

// Event label values for e2eSeconds. v0.3.0 emits "create" only; future minor
// releases may additively introduce "source-update", "self-heal",
// "ns-flip-add", "ns-flip-cleanup". Per docs/api-stability.md, additive label
// values on existing pre-v1.0 metrics are an expected evolution.
const eventCreate = "create"

// reconcileTotal counts reconcile outcomes partitioned by CR kind
// (Projection / ClusterProjection) and result bucket. Registered on
// controller-runtime's global metrics registry so it is exposed automatically
// on the manager's :8443/metrics endpoint.
var reconcileTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "projection_reconcile_total",
		Help: "Total reconciles partitioned by CR kind and outcome.",
	},
	[]string{"kind", "result"},
)

// watchedGvks counts source-watch registrations across both reconcilers.
// Each reconciler keeps its own dedup map keyed on the full GroupVersionKind,
// and increments this gauge once per first-seen GVK; a single GVK referenced
// from both Projection and ClusterProjection therefore contributes two. The
// underlying controller-runtime informer is shared per GVK, so this gauge
// approximates handler-chain count, not apiserver List/Watch streams.
// Intentionally a Gauge (not a Counter) because a future design may prune
// entries; keeping the type stable means existing dashboards don't have to
// change if that happens.
var watchedGvks = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "projection_watched_gvks",
		Help: "Source-watch registrations across reconcilers. One per (reconciler, source GroupVersionKind) pair.",
	},
)

// watchedDestGvks counts destination-watch registrations across both
// reconcilers via ensureDestWatch. Same per-(reconciler, GVK) accounting
// as watchedGvks — a destination GVK touched by both reconcilers contributes
// two. Same Gauge rationale as watchedGvks: the value rises monotonically
// today but the type leaves room for pruning later.
var watchedDestGvks = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "projection_watched_dest_gvks",
		Help: "Destination-watch registrations across reconcilers (ensureDestWatch). One per (reconciler, destination GroupVersionKind) pair.",
	},
)

// e2eSeconds is the wall-clock latency from a Projection or
// ClusterProjection's metadata.creationTimestamp to the first successful
// destination Create. Companion to the bench harness in test/bench/, which
// measures the same observation externally — exposing it as a controller
// metric lets production dashboards read what the bench reports.
//
// The `event` label is reserved for additive values in future releases (e.g.
// "source-update", "self-heal", "ns-flip-add", "ns-flip-cleanup"); v0.3.0
// emits "create" only. Buckets are locked at v1.0 and span 5ms..30s — sized
// for the typical create-path floor of a few-tens-of-ms through the slow
// end of multi-second apiserver-throttled reconciles.
var e2eSeconds = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "projection_e2e_seconds",
		Help:    "End-to-end latency from CR creationTimestamp to first successful destination Create.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	},
	[]string{"kind", "event"},
)

func init() {
	metrics.Registry.MustRegister(reconcileTotal, watchedGvks, watchedDestGvks, e2eSeconds)
}
