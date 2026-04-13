/*
Copyright 2024.

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

// reconcileTotal counts Projection reconcile outcomes. Registered on
// controller-runtime's global metrics registry so it is exposed automatically
// on the manager's :8443/metrics endpoint.
var reconcileTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "projection_reconcile_total",
		Help: "Total Projection reconciles partitioned by outcome.",
	},
	[]string{"result"},
)

func init() {
	metrics.Registry.MustRegister(reconcileTotal)
}
