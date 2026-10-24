// Package vars holds the per-cycle, per-target variable map shared by steps.
package vars

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// minSecretLen avoids masking half of every log line with a tiny secret.
const minSecretLen = 4

// Value is a variable value. Raw is whatever the source produced: a YAML
// scalar (string, int, float64, bool), a captured value or a map such as a
// payload's {sha256, size}.
type Value struct {
	Raw    any
	Secret bool
}

// Vars is not safe for concurrent use: the steps of a target run in sequence.
type Vars struct {
	m       map[string]Value
	secrets []string
}

func New() *Vars { return &Vars{m: map[string]Value{}} }

// Set stores a value; the last write wins.
func (v *Vars) Set(name string, val Value) {
	v.m[name] = val
	if !val.Secret {
		return
	}
	s := fmt.Sprint(val.Raw)
	if len(s) < minSecretLen {
		slog.Warn("secret too short to be redacted from logs", "var", name)
		return
	}
	v.secrets = append(v.secrets, s)
	// Longest first, so a secret that contains another is masked whole.
	slices.SortFunc(v.secrets, func(a, b string) int { return len(b) - len(a) })
}

func (v *Vars) Get(name string) (Value, bool) {
	val, ok := v.m[name]
	return val, ok
}

// Data returns the raw values, as seen by templates under .vars.
func (v *Vars) Data() map[string]any {
	d := make(map[string]any, len(v.m))
	for k, val := range v.m {
		d[k] = val.Raw
	}
	return d
}

// Redact masks every secret value found in s.
func (v *Vars) Redact(s string) string {
	if len(v.secrets) == 0 {
		return s
	}
	pairs := make([]string, 0, 2*len(v.secrets))
	for _, sec := range v.secrets {
		pairs = append(pairs, sec, "***")
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
