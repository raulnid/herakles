package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"

	"github.com/raulnid/herakles/internal/config"
	"github.com/raulnid/herakles/internal/module"
	"github.com/raulnid/herakles/internal/tmpl"
	"github.com/raulnid/herakles/internal/vars"
)

// run is the state of one cycle of one target. Nothing survives the cycle.
type run struct {
	e      *Engine
	target config.Target
	vars   *vars.Vars
	cycle  map[string]any
	client *http.Client
	// unavailable holds outputs of skipped steps: steps that need them are
	// skipped too instead of failing.
	unavailable map[string]bool
}

// cycle runs every labor of one target once and reports whether all required
// steps passed.
func (e *Engine) cycle(ctx context.Context, t config.Target) bool {
	start := time.Now()
	v, err := vars.Resolve(e.cfg.Vars, t.Vars)
	if err != nil {
		e.obs.CycleEnd(CycleReport{Target: t.Name, Err: err, Duration: time.Since(start)})
		return false
	}
	r := &run{
		e: e, target: t, vars: v,
		cycle:       map[string]any{"id": fmt.Sprintf("%08x", rand.Uint32()), "start": start.Unix()},
		client:      &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()},
		unavailable: map[string]bool{},
	}
	defer r.client.CloseIdleConnections()

	ok := true
	var stoppedBy string
	for _, l := range e.cfg.Labors {
		if l.Finalizer {
			continue
		}
		reason := ""
		switch {
		case stoppedBy != "":
			reason = fmt.Sprintf("labor %q failed", stoppedBy)
		case ctx.Err() != nil:
			reason = "shutting down"
		}
		if reason != "" {
			e.obs.LaborEnd(LaborReport{Target: t.Name, Labor: l.Name, Status: Skipped, Reason: reason, Steps: stepNames(l)})
			continue
		}
		if r.labor(ctx, l) == Failed {
			ok, stoppedBy = false, l.Name
		}
	}
	// Finalizers (logout and the like) run whatever happened, even on shutdown.
	for _, l := range e.cfg.Labors {
		if l.Finalizer && r.labor(context.WithoutCancel(ctx), l) == Failed {
			ok = false
		}
	}
	e.obs.CycleEnd(CycleReport{Target: t.Name, OK: ok, Duration: time.Since(start)})
	return ok
}

func (r *run) labor(ctx context.Context, l config.Labor) Status {
	start := time.Now()
	rep := LaborReport{Target: r.target.Name, Labor: l.Name, Steps: stepNames(l)}
	for i, s := range l.Steps {
		sr := r.step(ctx, l, s, i+1, len(l.Steps), l.Finalizer)
		if sr.Status == Failed && !s.Optional {
			rep.Status, rep.FailedStep, rep.Reason = Failed, s.Name, sr.Reason
			break
		}
	}
	// finally is a labor-level defer: it runs on failure and on shutdown.
	for _, s := range l.Finally {
		sr := r.step(context.WithoutCancel(ctx), l, s, 0, 0, true)
		if sr.Status == Failed && !s.Optional && rep.Status == OK {
			rep.Status, rep.FailedStep, rep.Reason = Failed, s.Name, sr.Reason
		}
	}
	if rep.Status == OK && ctx.Err() != nil {
		rep.Status, rep.Reason = Skipped, "shutting down"
	}
	rep.Duration = time.Since(start)
	r.e.obs.LaborEnd(rep)
	return rep.Status
}

// step runs one step. lenient steps (finally, finalizers) are skipped instead
// of failed when a variable is undefined: there is nothing to clean up.
func (r *run) step(ctx context.Context, l config.Labor, s config.Step, n, total int, lenient bool) StepReport {
	rep := StepReport{
		Target: r.target.Name, Labor: l.Name, Step: s.Name, Description: s.Description,
		N: n, Total: total, Finally: n == 0, Optional: s.Optional,
	}
	end := func(st Status, reason string) StepReport {
		rep.Status, rep.Reason = st, reason
		r.e.obs.StepEnd(rep)
		return rep
	}

	m, ok := module.Get(s.Module)
	if !ok {
		return end(Failed, fmt.Sprintf("unknown module %q", s.Module))
	}
	skip := func(reason string) StepReport {
		r.markUnavailable(m, s)
		return end(Skipped, reason)
	}
	if r.target.Safe && s.Mutating {
		return skip("mutating step on a safe target")
	}
	if ctx.Err() != nil {
		return skip("shutting down")
	}

	rendered, err := tmpl.Render(&s.Config, map[string]any{"vars": r.vars.Data(), "target": r.target.Name, "cycle": r.cycle})
	if undef := (*tmpl.UndefinedError)(nil); errors.As(err, &undef) && (lenient || r.unavailable[undef.Name]) {
		return skip(fmt.Sprintf("needs %q, which was not set", undef.Name))
	}
	if err != nil {
		return end(Failed, r.vars.Redact(err.Error()))
	}
	cfg := m.NewConfig()
	if err := module.Decode(rendered, cfg); err != nil {
		return end(Failed, r.vars.Redact("config: "+err.Error()))
	}

	timeout := r.e.cfg.StepTimeout
	if tr, ok := m.(module.Transferer); ok && tr.IsTransfer(cfg) {
		timeout = r.e.cfg.TransferTimeout
	}
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r.e.obs.StepStart(rep)
	t0 := time.Now()
	res := m.Run(sctx, cfg, &module.Env{Target: r.target.Name, Vars: r.vars, HTTPClient: r.client, UserAgent: r.e.cfg.UserAgent})
	rep.Duration, rep.Phases, rep.Transfers = time.Since(t0), res.Phases, res.Transfers

	switch {
	case res.Err == nil:
		for name, v := range res.Captured {
			r.vars.Set(name, v)
			delete(r.unavailable, name)
			rep.Captured = append(rep.Captured, name)
		}
		slices.Sort(rep.Captured)
		return end(OK, "")
	case ctx.Err() != nil:
		// Interrupted by shutdown, not a real failure of the target.
		return skip("shutting down")
	case errors.Is(sctx.Err(), context.DeadlineExceeded):
		return end(Failed, r.vars.Redact(fmt.Sprintf("timeout after %s: %v", timeout, res.Err)))
	default:
		return end(Failed, r.vars.Redact(res.Err.Error()))
	}
}

func (r *run) markUnavailable(m module.Module, s config.Step) {
	o, ok := m.(module.Outputs)
	if !ok {
		return
	}
	cfg := m.NewConfig()
	if module.Decode(tmpl.Static(&s.Config), cfg) != nil {
		return
	}
	for _, name := range o.Outputs(cfg) {
		r.unavailable[name] = true
	}
}

func stepNames(l config.Labor) []string {
	names := make([]string, 0, len(l.Steps)+len(l.Finally))
	for _, s := range append(slices.Clone(l.Steps), l.Finally...) {
		names = append(names, s.Name)
	}
	return names
}
