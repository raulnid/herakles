package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/raulnid/herakles/internal/engine"
	"github.com/raulnid/herakles/internal/module"
)

func TestSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)

	m.StepEnd(engine.StepReport{Target: "dev", Labor: "projects", Step: "upload", Status: engine.OK, Duration: 1500 * time.Millisecond,
		Phases:    map[string]time.Duration{"wait": 20 * time.Millisecond, "tls": 0},
		Transfers: []module.Transfer{{Direction: "upload", Bytes: 6 << 20, Duration: time.Second}}})
	m.StepEnd(engine.StepReport{Target: "dev", Labor: "projects", Step: "download", Status: engine.Failed, Duration: 3 * time.Millisecond})
	m.StepEnd(engine.StepReport{Target: "prod", Labor: "projects", Step: "upload", Status: engine.Skipped})
	m.LaborEnd(engine.LaborReport{Target: "dev", Labor: "projects", Status: engine.Failed, FailedStep: "download",
		Duration: 2 * time.Second, Steps: []string{"upload", "download", "cleanup"}})
	m.LaborEnd(engine.LaborReport{Target: "dev", Labor: "after", Status: engine.Skipped})
	m.CycleEnd(engine.CycleReport{Target: "dev", OK: false})

	want := `
# HELP herakles_step_success 1 if the last attempt of the step passed, 0 if it failed. Includes optional steps.
# TYPE herakles_step_success gauge
herakles_step_success{labor="projects",step="download",target="dev"} 0
herakles_step_success{labor="projects",step="upload",target="dev"} 1
# HELP herakles_step_skipped 1 if the step was skipped in its last cycle (mutating step on a safe target, missing input).
# TYPE herakles_step_skipped gauge
herakles_step_skipped{labor="projects",step="download",target="dev"} 0
herakles_step_skipped{labor="projects",step="upload",target="dev"} 0
herakles_step_skipped{labor="projects",step="upload",target="prod"} 1
# HELP herakles_step_last_duration_seconds Exact duration of the last attempt of the step.
# TYPE herakles_step_last_duration_seconds gauge
herakles_step_last_duration_seconds{labor="projects",step="download",target="dev"} 0.003
herakles_step_last_duration_seconds{labor="projects",step="upload",target="dev"} 1.5
# HELP herakles_http_phase_seconds Phases of the last HTTP request of the step: dns, connect, tls, wait, transfer, total.
# TYPE herakles_http_phase_seconds gauge
herakles_http_phase_seconds{labor="projects",phase="tls",step="upload",target="dev"} 0
herakles_http_phase_seconds{labor="projects",phase="wait",step="upload",target="dev"} 0.02
# HELP herakles_transfer_bytes_per_second Rate of the last upload or download of the step.
# TYPE herakles_transfer_bytes_per_second gauge
herakles_transfer_bytes_per_second{direction="upload",labor="projects",step="upload",target="dev"} 6.291456e+06
# HELP herakles_labor_success 1 if the last run of the labor passed.
# TYPE herakles_labor_success gauge
herakles_labor_success{labor="projects",target="dev"} 0
# HELP herakles_labor_failed_step 1 on the required step that failed the last run of the labor, 0 on the others.
# TYPE herakles_labor_failed_step gauge
herakles_labor_failed_step{labor="projects",step="cleanup",target="dev"} 0
herakles_labor_failed_step{labor="projects",step="download",target="dev"} 1
herakles_labor_failed_step{labor="projects",step="upload",target="dev"} 0
# HELP herakles_cycle_success 1 if every required step of the last cycle passed. 0 also when variables could not be resolved.
# TYPE herakles_cycle_success gauge
herakles_cycle_success{target="dev"} 0
`
	names := []string{"herakles_step_success", "herakles_step_skipped", "herakles_step_last_duration_seconds", "herakles_http_phase_seconds",
		"herakles_transfer_bytes_per_second", "herakles_labor_success", "herakles_labor_failed_step", "herakles_cycle_success"}
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), names...); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(m.laborLastSuccess); n != 0 {
		t.Errorf("a failed labor must not set its last success, got %d series", n)
	}
	if n := testutil.CollectAndCount(m.stepDuration); n != 2 {
		t.Errorf("skipped steps must not be observed: %d histogram series", n)
	}

	// The next run passes: failed_step clears, last success is set.
	m.LaborEnd(engine.LaborReport{Target: "dev", Labor: "projects", Status: engine.OK, Steps: []string{"upload", "download", "cleanup"}})
	if v := testutil.ToFloat64(m.laborFailedStep.WithLabelValues("dev", "projects", "download")); v != 0 {
		t.Errorf("failed_step not cleared: %v", v)
	}
	if v := testutil.ToFloat64(m.laborLastSuccess.WithLabelValues("dev", "projects")); v < float64(time.Now().Add(-time.Minute).Unix()) {
		t.Errorf("last success timestamp = %v", v)
	}
}
