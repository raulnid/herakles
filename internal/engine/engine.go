// Package engine runs labors: one independent loop per target, a fresh
// variable map per cycle, finally blocks and finalizer labors.
package engine

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raulnid/herakles/internal/config"
	"github.com/raulnid/herakles/internal/tmpl"
)

type Engine struct {
	cfg *config.Config
	obs Observer
	// Derived once from the immutable config instead of on every cycle.
	needs map[*config.Step][]string
	names map[*config.Labor][]string
}

func New(cfg *config.Config, obs Observer) *Engine {
	if obs == nil {
		obs = Observers{}
	}
	e := &Engine{cfg: cfg, obs: obs, needs: map[*config.Step][]string{}, names: map[*config.Labor][]string{}}
	for li := range cfg.Labors {
		l := &cfg.Labors[li]
		for _, steps := range [][]config.Step{l.Steps, l.Finally} {
			for si := range steps {
				s := &steps[si]
				e.needs[s], _ = tmpl.NodeRefs(&s.Config) // a malformed template fails when it renders
				e.names[l] = append(e.names[l], s.Name)
			}
		}
	}
	return e
}

// Once runs one cycle of every target in parallel and reports whether all
// required steps passed.
func (e *Engine) Once(ctx context.Context) bool {
	var ok atomic.Bool
	ok.Store(true)
	e.eachTarget(func(t config.Target) {
		if !e.cycle(ctx, t) {
			ok.Store(false)
		}
	})
	return ok.Load()
}

// Run starts one loop per target and blocks until ctx is cancelled and every
// in-flight cycle has run its cleanup.
func (e *Engine) Run(ctx context.Context) {
	e.eachTarget(func(t config.Target) {
		for {
			start := time.Now()
			e.cycle(ctx, t)
			if ctx.Err() != nil {
				return // select below may pick a zero wait over Done and start a ghost cycle
			}
			wait := e.cfg.Interval - time.Since(start)
			if wait < 0 {
				slog.Warn("cycle took longer than interval, starting the next one now",
					"target", t.Name, "took", time.Since(start).Round(time.Millisecond), "interval", e.cfg.Interval)
				wait = 0
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	})
}

func (e *Engine) eachTarget(fn func(config.Target)) {
	var wg sync.WaitGroup
	for _, t := range e.cfg.Targets {
		wg.Go(func() { fn(t) })
	}
	wg.Wait()
}
