package engine

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/raulnid/herakles/internal/config"
	"github.com/raulnid/herakles/internal/module"
	"github.com/raulnid/herakles/internal/tmpl"
)

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks what config.Parse cannot: module names and configs,
// template syntax, and that every variable a step reads is declared or set by
// an earlier step, for every target. Nothing is executed.
func Validate(cfg *config.Config) error {
	var errs []error
	outputs := map[*config.Step][]string{}
	all := map[string]bool{} // every variable set anywhere, for finally and finalizers
	for li := range cfg.Labors {
		l := &cfg.Labors[li]
		for si, s := range slices.Concat(l.Steps, l.Finally) {
			where := fmt.Sprintf("labor %q step %q", l.Name, s.Name)
			outs, err := checkStep(s)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", where, err))
			}
			outputs[stepAt(l, si)] = outs
			for _, o := range outs {
				all[o] = true
			}
		}
	}

	// undefined[message] lists the targets where a reference is undefined.
	undefined := map[string][]string{}
	var order []string
	for _, t := range cfg.Targets {
		defined := map[string]bool{}
		for name := range maps.Keys(cfg.Vars) {
			defined[name] = true
		}
		for name := range maps.Keys(t.Vars) {
			defined[name] = true
		}
		check := func(l *config.Labor, s *config.Step, lenient bool) {
			refs, _ := tmpl.NodeRefs(&s.Config) // syntax errors are reported by checkStep
			for _, r := range refs {
				if defined[r] || (lenient && all[r]) {
					continue
				}
				msg := fmt.Sprintf("labor %q step %q: variable %q is not declared in vars nor set by an earlier step", l.Name, s.Name, r)
				if undefined[msg] == nil {
					order = append(order, msg)
				}
				undefined[msg] = append(undefined[msg], t.Name)
			}
		}
		run := func(finalizers bool) {
			for li := range cfg.Labors {
				l := &cfg.Labors[li]
				if l.Finalizer != finalizers {
					continue
				}
				for si := range l.Steps {
					check(l, &l.Steps[si], finalizers)
					for _, o := range outputs[&l.Steps[si]] {
						defined[o] = true
					}
				}
				for si := range l.Finally {
					check(l, &l.Finally[si], true)
				}
			}
		}
		run(false)
		run(true)
	}
	for _, msg := range order {
		if ts := undefined[msg]; len(ts) < len(cfg.Targets) {
			msg += " (target " + strings.Join(ts, ", ") + ")"
		}
		errs = append(errs, errors.New(msg))
	}
	return errors.Join(errs...)
}

func stepAt(l *config.Labor, i int) *config.Step {
	if i < len(l.Steps) {
		return &l.Steps[i]
	}
	return &l.Finally[i-len(l.Steps)]
}

// checkStep validates one step statically and returns the variables it sets.
func checkStep(s config.Step) ([]string, error) {
	m, ok := module.Get(s.Module)
	if !ok {
		return nil, fmt.Errorf("unknown module %q (available: %s)", s.Module, strings.Join(module.Names(), ", "))
	}
	if _, err := tmpl.NodeRefs(&s.Config); err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	cfg := m.NewConfig()
	if err := module.Decode(tmpl.Static(&s.Config), cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Module, err)
	}
	var errs []error
	if v, ok := m.(module.Validator); ok {
		if err := v.Validate(cfg); err != nil {
			errs = append(errs, err)
		}
	}
	var outs []string
	if o, ok := m.(module.Outputs); ok {
		outs = o.Outputs(cfg)
		slices.Sort(outs)
		for _, name := range outs {
			if !identRe.MatchString(name) {
				errs = append(errs, fmt.Errorf("output %q: name must match %s", name, identRe))
			}
		}
	}
	return outs, errors.Join(errs...)
}
