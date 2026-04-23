// Package metrics declares the Prometheus collectors exposed by the mastermind.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	TasksClaimedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tasks_claimed_total",
		Help: "Total number of ClaimTask RPCs served, labelled by result.",
	}, []string{"result"})

	TasksCompletedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tasks_completed_total",
		Help: "Total number of tasks that completed successfully.",
	})

	TasksFailedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "tasks_failed_total",
		Help: "Total number of tasks that reached the failed terminal state.",
	}, []string{"reason"})

	TasksReapedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "tasks_reaped_total",
		Help: "Total number of tasks reclaimed by the reaper.",
	})

	TasksPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "tasks_pending",
		Help: "Current number of tasks in the pending state.",
	})

	ClaimDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "claim_duration_seconds",
		Help:    "Latency of the ClaimTask SQL.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
	})
)
