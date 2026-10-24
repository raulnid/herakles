package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// varName keeps names usable as {{ .vars.name }} in templates.
var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load reads, decodes and structurally validates a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Config, error) {
	c := &Config{
		Interval:        DefaultInterval,
		StepTimeout:     DefaultStepTimeout,
		TransferTimeout: DefaultTransferTimeout,
		Listen:          DefaultListen,
		UserAgent:       DefaultUserAgent,
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// OnlyTarget drops every target except name.
func (c *Config) OnlyTarget(name string) error {
	for _, t := range c.Targets {
		if t.Name == name {
			c.Targets = []Target{t}
			return nil
		}
	}
	return fmt.Errorf("target %q not found", name)
}

func (c *Config) validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Interval <= 0 {
		add("interval must be > 0")
	}
	if c.StepTimeout <= 0 {
		add("step_timeout must be > 0")
	}
	if c.TransferTimeout <= 0 {
		add("transfer_timeout must be > 0")
	}
	checkVars := func(where string, vs map[string]Var) {
		for name, v := range vs {
			if !varName.MatchString(name) {
				add("%s: var %q: name must match %s", where, name, varName)
			}
			n := 0
			for _, set := range []bool{v.Value != nil, v.File != "", v.Env != ""} {
				if set {
					n++
				}
			}
			if n != 1 {
				add("%s: var %q: exactly one of value, file or env is required", where, name)
			}
		}
	}
	checkVars("vars", c.Vars)

	if len(c.Targets) == 0 {
		add("at least one target is required")
	}
	targets := map[string]bool{}
	for i, t := range c.Targets {
		if t.Name == "" {
			add("targets[%d]: name is required", i)
		} else if targets[t.Name] {
			add("target %q: duplicate name", t.Name)
		}
		targets[t.Name] = true
		checkVars("target "+t.Name, t.Vars)
	}

	if len(c.Labors) == 0 {
		add("at least one labor is required")
	}
	labors := map[string]bool{}
	for i, l := range c.Labors {
		if l.Name == "" {
			add("labors[%d]: name is required", i)
		} else if labors[l.Name] {
			add("labor %q: duplicate name", l.Name)
		}
		labors[l.Name] = true
		if len(l.Steps) == 0 {
			add("labor %q: at least one step is required", l.Name)
		}
		// steps and finally share the step metric label, so names are unique across both.
		steps := map[string]bool{}
		for j, s := range append(append([]Step{}, l.Steps...), l.Finally...) {
			if s.Name == "" {
				add("labor %q: step %d: name is required", l.Name, j)
			} else if steps[s.Name] {
				add("labor %q: step %q: duplicate name", l.Name, s.Name)
			}
			steps[s.Name] = true
		}
	}
	return errors.Join(errs...)
}
