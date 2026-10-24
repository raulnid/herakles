package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raulnid/herakles/internal/config"
	"github.com/raulnid/herakles/internal/module"
	"github.com/raulnid/herakles/internal/vars"
)

// fake is a test module: it can sleep, fail, capture and record that it ran.
type fake struct{}

type fakeCfg struct {
	Echo     string            `yaml:"echo"`
	Fail     string            `yaml:"fail"`
	Sleep    time.Duration     `yaml:"sleep"`
	Set      map[string]string `yaml:"set"`
	Secret   bool              `yaml:"secret"`
	Transfer bool              `yaml:"transfer"`
}

var (
	ranMu sync.Mutex
	ran   []string
)

func init() { module.Register("fake", fake{}) }

func (fake) NewConfig() any { return &fakeCfg{} }

func (fake) Outputs(cfg any) []string {
	var out []string
	for k := range cfg.(*fakeCfg).Set {
		out = append(out, k)
	}
	return out
}

func (fake) IsTransfer(cfg any) bool { return cfg.(*fakeCfg).Transfer }

func (fake) Run(ctx context.Context, cfg any, env *module.Env) module.Result {
	c := cfg.(*fakeCfg)
	if c.Echo != "" {
		ranMu.Lock()
		ran = append(ran, c.Echo)
		ranMu.Unlock()
	}
	select {
	case <-time.After(c.Sleep):
	case <-ctx.Done():
		return module.Result{Err: ctx.Err()}
	}
	if c.Fail != "" {
		return module.Result{Err: errors.New(c.Fail)}
	}
	res := module.Result{Captured: map[string]vars.Value{}}
	for k, v := range c.Set {
		res.Captured[k] = vars.Value{Raw: v, Secret: c.Secret}
	}
	return res
}

func ranSteps() []string {
	ranMu.Lock()
	defer ranMu.Unlock()
	out := slices.Clone(ran)
	ran = nil
	return out
}

// recorder is an Observer that keeps every event.
type recorder struct {
	mu     sync.Mutex
	steps  []StepReport
	starts int
	labors []LaborReport
	cycles []CycleReport
}

func (r *recorder) StepStart(StepReport) { r.mu.Lock(); r.starts++; r.mu.Unlock() }
func (r *recorder) StepEnd(s StepReport) { r.mu.Lock(); r.steps = append(r.steps, s); r.mu.Unlock() }
func (r *recorder) LaborEnd(l LaborReport) {
	r.mu.Lock()
	r.labors = append(r.labors, l)
	r.mu.Unlock()
}
func (r *recorder) CycleEnd(c CycleReport) {
	r.mu.Lock()
	r.cycles = append(r.cycles, c)
	r.mu.Unlock()
}

func (r *recorder) step(target, labor, step string) StepReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.steps {
		if s.Target == target && s.Labor == labor && s.Step == step {
			return s
		}
	}
	return StepReport{Status: -1}
}

func (r *recorder) labor(target, labor string) LaborReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.labors {
		if l.Target == target && l.Labor == labor {
			return l
		}
	}
	return LaborReport{Status: -1}
}

func setup(t *testing.T, yaml string) (*Engine, *recorder) {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	ranSteps()
	rec := &recorder{}
	return New(cfg, rec), rec
}

func TestFailureSemantics(t *testing.T) {
	e, rec := setup(t, `
targets: [{name: dev}]
labors:
  - name: login
    steps:
      - {name: token, fake: {set: {token: t0k3n-value}, secret: true}}
      - {name: extra, optional: true, fake: {fail: "flaky endpoint"}}
      - {name: profile, fake: {echo: profile}}
  - name: projects
    steps:
      - {name: upload, fake: {set: {id: "42"}}}
      - {name: broken, fake: {fail: "boom with {{ .vars.token }}"}}
      - {name: never, fake: {echo: never}}
    finally:
      - {name: cleanup, fake: {echo: "cleanup {{ .vars.id }}"}}
      - {name: cleanup-missing, fake: {echo: "{{ .vars.nope }}"}}
  - name: after
    steps: [{name: x, fake: {echo: after}}]
  - name: logout
    finalizer: true
    steps:
      - {name: logout, fake: {echo: "logout {{ .vars.token }}"}}
`)
	if e.Once(context.Background()) {
		t.Error("Once = true, want false: a required step failed")
	}
	if got, want := ranSteps(), []string{"profile", "cleanup 42", "logout t0k3n-value"}; !slices.Equal(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
	if s := rec.step("dev", "login", "extra"); s.Status != Failed || !s.Optional || s.N != 2 || s.Total != 3 {
		t.Errorf("optional step: %+v", s)
	}
	if l := rec.labor("dev", "login"); l.Status != OK {
		t.Errorf("optional failure must not fail the labor: %+v", l)
	}
	l := rec.labor("dev", "projects")
	if l.Status != Failed || l.FailedStep != "broken" {
		t.Errorf("projects labor: %+v", l)
	}
	if strings.Contains(l.Reason, "t0k3n-value") || !strings.Contains(l.Reason, "boom with ***") {
		t.Errorf("secret not redacted: %q", l.Reason)
	}
	if s := rec.step("dev", "projects", "cleanup-missing"); s.Status != Skipped || !s.Finally {
		t.Errorf("finally step with undefined var must be skipped: %+v", s)
	}
	if l := rec.labor("dev", "after"); l.Status != Skipped || !strings.Contains(l.Reason, `"projects" failed`) {
		t.Errorf("labor after a failure must be skipped: %+v", l)
	}
	if l := rec.labor("dev", "logout"); l.Status != OK {
		t.Errorf("finalizer must run and pass: %+v", l)
	}
	if s := rec.step("dev", "login", "token"); !slices.Equal(s.Captured, []string{"token"}) {
		t.Errorf("captured names: %v", s.Captured)
	}
	if c := rec.cycles; len(c) != 1 || c[0].OK {
		t.Errorf("cycles: %+v", c)
	}
}

func TestSafeTargetSkipsMutating(t *testing.T) {
	e, rec := setup(t, `
targets: [{name: prod, safe: true}]
labors:
  - name: projects
    steps:
      - {name: list, fake: {echo: list}}
      - {name: upload, mutating: true, fake: {set: {id: "1"}}}
      - {name: download, fake: {echo: "get {{ .vars.id }}"}}
`)
	if !e.Once(context.Background()) {
		t.Error("Once = false, want true")
	}
	if got := ranSteps(); !slices.Equal(got, []string{"list"}) {
		t.Errorf("ran %v", got)
	}
	if s := rec.step("prod", "projects", "upload"); s.Status != Skipped || !strings.Contains(s.Reason, "safe") {
		t.Errorf("upload: %+v", s)
	}
	if s := rec.step("prod", "projects", "download"); s.Status != Skipped {
		t.Errorf("step needing an output of a skipped step must be skipped: %+v", s)
	}
	if l := rec.labor("prod", "projects"); l.Status != OK {
		t.Errorf("labor: %+v", l)
	}
	if rec.starts != 1 {
		t.Errorf("StepStart called %d times, want 1 (skipped steps do not start)", rec.starts)
	}
}

func TestUndefinedVarFailsRequiredStep(t *testing.T) {
	e, rec := setup(t, `
targets: [{name: dev}]
labors:
  - name: l
    steps: [{name: s, fake: {echo: "{{ .vars.ghost }}"}}]
`)
	e.Once(context.Background())
	if s := rec.step("dev", "l", "s"); s.Status != Failed || !strings.Contains(s.Reason, `undefined variable "ghost"`) {
		t.Errorf("step: %+v", s)
	}
}

func TestEachCycleStartsEmpty(t *testing.T) {
	e, rec := setup(t, `
targets: [{name: dev}]
labors:
  - name: l
    steps:
      - {name: read, optional: true, fake: {echo: "{{ .vars.carried }}"}}
      - {name: write, fake: {set: {carried: "yes"}}}
`)
	e.Once(context.Background())
	rec.steps = nil
	e.Once(context.Background())
	if s := rec.step("dev", "l", "read"); s.Status != Failed {
		t.Errorf("second cycle saw a variable from the first one: %+v", s)
	}
}

func TestTargetsAreIndependent(t *testing.T) {
	e, rec := setup(t, `
targets:
  - {name: slow, vars: {delay: {value: 300ms}}}
  - {name: fast, vars: {delay: {value: 0s}}}
labors:
  - name: l
    steps: [{name: s, fake: {sleep: "{{ .vars.delay }}"}}]
`)
	start := time.Now()
	e.Once(context.Background())
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Errorf("targets did not run in parallel: %s", took)
	}
	if rec.cycles[0].Target != "fast" || rec.cycles[0].Duration > 100*time.Millisecond {
		t.Errorf("fast target was delayed by the slow one: %+v", rec.cycles)
	}
}

func TestTimeouts(t *testing.T) {
	e, rec := setup(t, `
step_timeout: 50ms
transfer_timeout: 2s
targets: [{name: dev}]
labors:
  - name: l
    steps:
      - {name: bulk, fake: {sleep: 150ms, transfer: true}}
      - {name: hang, fake: {sleep: 5s}}
`)
	e.Once(context.Background())
	if s := rec.step("dev", "l", "bulk"); s.Status != OK {
		t.Errorf("transfer step must use transfer_timeout: %+v", s)
	}
	s := rec.step("dev", "l", "hang")
	if s.Status != Failed || !strings.Contains(s.Reason, "timeout after 50ms") || s.Duration > time.Second {
		t.Errorf("hang: %+v", s)
	}
}

func TestVarsErrorFailsCycle(t *testing.T) {
	e, rec := setup(t, `
vars: {key: {file: /nonexistent/herakles-key}}
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, fake: {echo: s}}]}]
`)
	if e.Once(context.Background()) {
		t.Error("Once = true, want false")
	}
	if len(rec.cycles) != 1 || rec.cycles[0].Err == nil || len(rec.labors) != 0 {
		t.Errorf("cycles %+v labors %+v", rec.cycles, rec.labors)
	}
}

func TestUnknownModule(t *testing.T) {
	e, rec := setup(t, `
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, nope: {}}]}]
`)
	e.Once(context.Background())
	if s := rec.step("dev", "l", "s"); s.Status != Failed || !strings.Contains(s.Reason, `unknown module "nope"`) {
		t.Errorf("step: %+v", s)
	}
}

func TestRequiredFinallyFailureFailsLabor(t *testing.T) {
	e, rec := setup(t, `
targets: [{name: dev}]
labors:
  - name: l
    steps: [{name: s, fake: {}}]
    finally: [{name: c, fake: {fail: leaked}}]
`)
	if e.Once(context.Background()) {
		t.Error("Once = true, want false")
	}
	if l := rec.labor("dev", "l"); l.Status != Failed || l.FailedStep != "c" {
		t.Errorf("labor: %+v", l)
	}
}

func TestShutdownRunsCleanup(t *testing.T) {
	e, rec := setup(t, `
interval: 10ms
vars: {sleep: {env: HERAKLES_TEST_SLEEP}}
targets: [{name: dev}]
labors:
  - name: l
    steps:
      - {name: create, fake: {set: {id: "7"}}}
      - {name: wait, fake: {sleep: "{{ .vars.sleep }}"}}
    finally: [{name: cleanup, fake: {echo: "cleanup {{ .vars.id }}"}}]
  - name: next
    steps: [{name: x, fake: {echo: next}}]
  - name: logout
    finalizer: true
    steps: [{name: bye, fake: {echo: bye}}]
`)
	// The first cycles are fast; then a slow step is interrupted by shutdown.
	t.Setenv("HERAKLES_TEST_SLEEP", "0s")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	os.Setenv("HERAKLES_TEST_SLEEP", "10s")
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.cycles) < 2 {
		t.Errorf("Run must loop: %d cycles", len(rec.cycles))
	}
	got := ranSteps()
	if n := len(got); n < 2 || got[n-2] != "cleanup 7" || got[n-1] != "bye" {
		t.Errorf("last steps %v, want the interrupted labor's cleanup and the finalizer", got)
	}
	last := rec.labors[len(rec.labors)-3:]
	want := []string{"l skipped", "next skipped", "logout ok"}
	for i, l := range last {
		if s := fmt.Sprintf("%s %s", l.Labor, l.Status); s != want[i] {
			t.Errorf("labor %d = %q, want %q", i, s, want[i])
		}
	}
}
