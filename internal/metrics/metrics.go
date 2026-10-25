// Package metrics exposes engine events as Prometheus series.
//
// Every histogram has an exact last-value gauge next to it: probe requests
// take milliseconds, and percentiles interpolated inside a bucket mislead.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/raulnid/herakles/internal/engine"
)

const ns = "herakles"

// buckets span 5ms to about 5 minutes, enough for slow transfers.
var buckets = prometheus.ExponentialBuckets(0.005, 2, 17)

type Metrics struct {
	stepDuration     *prometheus.HistogramVec
	stepLastDuration *prometheus.GaugeVec
	stepSuccess      *prometheus.GaugeVec
	stepSkipped      *prometheus.GaugeVec
	laborSuccess     *prometheus.GaugeVec
	laborLastSuccess *prometheus.GaugeVec
	laborDuration    *prometheus.HistogramVec
	laborLastDur     *prometheus.GaugeVec
	laborFailedStep  *prometheus.GaugeVec
	httpPhase        *prometheus.GaugeVec
	transferRate     *prometheus.GaugeVec
	cycleSuccess     *prometheus.GaugeVec
}

var _ engine.Observer = (*Metrics)(nil)

// New creates the series and registers them on reg.
func New(reg prometheus.Registerer) *Metrics {
	step := []string{"target", "labor", "step"}
	labor := []string{"target", "labor"}
	gauge := func(name, help string, labels ...string) *prometheus.GaugeVec {
		g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: ns, Name: name, Help: help}, labels)
		reg.MustRegister(g)
		return g
	}
	hist := func(name, help string, labels ...string) *prometheus.HistogramVec {
		h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: ns, Name: name, Help: help, Buckets: buckets}, labels)
		reg.MustRegister(h)
		return h
	}
	return &Metrics{
		stepDuration:     hist("step_duration_seconds", "Duration of each step attempt.", step...),
		stepLastDuration: gauge("step_last_duration_seconds", "Exact duration of the last attempt of the step.", step...),
		stepSuccess:      gauge("step_success", "1 if the last attempt of the step passed, 0 if it failed. Includes optional steps.", step...),
		stepSkipped:      gauge("step_skipped", "1 if the step was skipped in its last cycle (mutating step on a safe target, missing input).", step...),
		laborSuccess:     gauge("labor_success", "1 if the last run of the labor passed.", labor...),
		laborLastSuccess: gauge("labor_last_success_timestamp_seconds", "Unix time of the last successful run of the labor.", labor...),
		laborDuration:    hist("labor_duration_seconds", "Duration of each labor run, finally included.", labor...),
		laborLastDur:     gauge("labor_last_duration_seconds", "Exact duration of the last run of the labor.", labor...),
		laborFailedStep:  gauge("labor_failed_step", "1 on the required step that failed the last run of the labor, 0 on the others.", step...),
		httpPhase:        gauge("http_phase_seconds", "Phases of the last HTTP request of the step: dns, connect, tls, wait, transfer, total.", append(step, "phase")...),
		transferRate:     gauge("transfer_bytes_per_second", "Rate of the last upload or download of the step.", append(step, "direction")...),
		cycleSuccess:     gauge("cycle_success", "1 if every required step of the last cycle passed. 0 also when variables could not be resolved.", "target"),
	}
}

func (m *Metrics) StepStart(engine.StepReport) {}

func (m *Metrics) StepEnd(r engine.StepReport) {
	l := prometheus.Labels{"target": r.Target, "labor": r.Labor, "step": r.Step}
	if r.Status == engine.Skipped {
		m.stepSkipped.With(l).Set(1)
		return
	}
	m.stepSkipped.With(l).Set(0)
	m.stepDuration.With(l).Observe(r.Duration.Seconds())
	m.stepLastDuration.With(l).Set(r.Duration.Seconds())
	m.stepSuccess.With(l).Set(bool01(r.Status == engine.OK))
	for phase, d := range r.Phases {
		m.httpPhase.WithLabelValues(r.Target, r.Labor, r.Step, phase).Set(d.Seconds())
	}
	for _, t := range r.Transfers {
		m.transferRate.WithLabelValues(r.Target, r.Labor, r.Step, t.Direction).Set(t.BytesPerSecond())
	}
}

// LaborEnd leaves the series of a skipped labor untouched: its last success
// timestamp ages, which is what a staleness alert watches.
func (m *Metrics) LaborEnd(r engine.LaborReport) {
	if r.Status == engine.Skipped {
		return
	}
	l := prometheus.Labels{"target": r.Target, "labor": r.Labor}
	m.laborSuccess.With(l).Set(bool01(r.Status == engine.OK))
	m.laborDuration.With(l).Observe(r.Duration.Seconds())
	m.laborLastDur.With(l).Set(r.Duration.Seconds())
	if r.Status == engine.OK {
		m.laborLastSuccess.With(l).Set(float64(time.Now().Unix()))
	}
	for _, s := range r.Steps {
		m.laborFailedStep.WithLabelValues(r.Target, r.Labor, s).Set(bool01(s == r.FailedStep))
	}
}

func (m *Metrics) CycleEnd(r engine.CycleReport) {
	m.cycleSuccess.WithLabelValues(r.Target).Set(bool01(r.OK))
}

func bool01(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
