// Package vars holds the per-cycle, per-target variable map shared by steps.
package vars

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
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
	raw     map[string]any
	secret  map[string]bool
	secrets []string
	redact  *strings.Replacer
}

func New() *Vars { return &Vars{raw: map[string]any{}, secret: map[string]bool{}} }

// Set stores a value; the last write wins.
func (v *Vars) Set(name string, val Value) {
	v.raw[name], v.secret[name] = val.Raw, val.Secret
	if !val.Secret {
		return
	}
	s := fmt.Sprint(val.Raw)
	if len(s) < minSecretLen {
		slog.Warn("secret too short to be redacted from logs", "var", name)
		return
	}
	// Errors quote URLs and bodies, so mask the encoded forms too.
	j, _ := json.Marshal(s)
	for _, form := range []string{s, url.QueryEscape(s), url.PathEscape(s), string(j[1 : len(j)-1])} {
		if !slices.Contains(v.secrets, form) {
			v.secrets = append(v.secrets, form)
		}
	}
	// Longest first, so a secret that contains another is masked whole.
	slices.SortFunc(v.secrets, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(v.secrets))
	for _, sec := range v.secrets {
		pairs = append(pairs, sec, "***")
	}
	v.redact = strings.NewReplacer(pairs...)
}

func (v *Vars) Get(name string) (Value, bool) {
	raw, ok := v.raw[name]
	return Value{Raw: raw, Secret: v.secret[name]}, ok
}

// Data returns the raw values, as seen by templates under .vars. It is the
// live map, not a copy: callers must not modify it.
func (v *Vars) Data() map[string]any { return v.raw }

// Redact masks every secret value found in s.
func (v *Vars) Redact(s string) string {
	if v.redact == nil {
		return s
	}
	return v.redact.Replace(s)
}
