package scheduledrun

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

type schedulerMetrics struct {
	activeSchedules prometheus.Gauge
	executions      *prometheus.CounterVec
	duration        prometheus.Histogram
}

func newMetrics(registerer prometheus.Registerer) (*schedulerMetrics, error) {
	m := &schedulerMetrics{
		activeSchedules: prometheus.NewGauge(prometheus.GaugeOpts{Name: "kagent_scheduled_runs_active", Help: "Number of active automatic schedules on the leader."}),
		executions:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kagent_scheduled_run_executions_total", Help: "Completed scheduled executions by outcome and trigger."}, []string{"status", "trigger"}),
		duration:        prometheus.NewHistogram(prometheus.HistogramOpts{Name: "kagent_scheduled_run_execution_duration_seconds", Help: "Time from execution reservation to terminal outcome.", Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 900}}),
	}
	if registerer != nil {
		for _, collector := range []prometheus.Collector{m.activeSchedules, m.executions, m.duration} {
			if err := registerer.Register(collector); err != nil {
				return nil, fmt.Errorf("failed to register scheduled run metrics: %w", err)
			}
		}
	}
	return m, nil
}
