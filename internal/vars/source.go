package vars

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/raulnid/herakles/internal/config"
)

// Resolve builds a fresh Vars for one cycle of one target. Target vars
// override global ones. Files and environment variables are read on every
// call, so a rotated secret is picked up on the next cycle.
func Resolve(global, target map[string]config.Var) (*Vars, error) {
	all := maps.Clone(global)
	if all == nil {
		all = map[string]config.Var{}
	}
	maps.Copy(all, target)

	v := New()
	var errs []error
	for name, src := range all {
		var raw any
		switch {
		case src.File != "":
			b, err := os.ReadFile(src.File)
			if err != nil {
				errs = append(errs, fmt.Errorf("var %q: %w", name, err))
				continue
			}
			raw = strings.TrimRight(string(b), "\r\n")
		case src.Env != "":
			s, ok := os.LookupEnv(src.Env)
			if !ok {
				errs = append(errs, fmt.Errorf("var %q: environment variable %s is not set", name, src.Env))
				continue
			}
			raw = s
		default:
			raw = src.Value
		}
		// File vars are always secret: they exist to keep secrets out of the YAML.
		v.Set(name, Value{Raw: raw, Secret: src.Secret || src.File != ""})
	}
	return v, errors.Join(errs...)
}
