package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadValid(t *testing.T) {
	c, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Interval != 30*time.Second || c.StepTimeout != DefaultStepTimeout || c.UserAgent != DefaultUserAgent {
		t.Errorf("defaults not applied: %+v", c)
	}
	if len(c.Targets) != 2 || !c.Targets[1].Safe {
		t.Errorf("targets: %+v", c.Targets)
	}
	up := c.Labors[1].Steps[0]
	if up.Module != "http" || !up.Mutating || up.Config.Kind == 0 {
		t.Errorf("step: %+v", up)
	}
	if !c.Labors[2].Finalizer || !c.Labors[1].Finally[0].Optional {
		t.Errorf("labor flags not decoded")
	}
	if c.Labors[0].Steps[0].Description == "" {
		t.Errorf("description not decoded")
	}
	if !c.Vars["api_key"].Secret {
		t.Errorf("secret flag not decoded")
	}
}

func TestLoadInvalidFileReportsAllErrors(t *testing.T) {
	_, err := Load("testdata/invalid.yaml")
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"interval must be > 0", `var "both": exactly one`, "at least one step"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

const base = `
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, http: {}}]}]
`

func TestParseErrors(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"unknown top field":   {base + "bogus: 1\n", "field bogus not found"},
		"bad duration":        {base + "interval: soon\n", "decode"},
		"no targets":          {"labors: [{name: l, steps: [{name: s, http: {}}]}]", "at least one target"},
		"no labors":           {"targets: [{name: dev}]", "at least one labor"},
		"minimal ok":          {base, ""},
		"two module keys":     {"targets: [{name: dev}]\nlabors: [{name: l, steps: [{name: s, http: {}, grpc: {}}]}]", "more than one module key"},
		"no module key":       {"targets: [{name: dev}]\nlabors: [{name: l, steps: [{name: s}]}]", "no module key"},
		"dup step in finally": {"targets: [{name: dev}]\nlabors: [{name: l, steps: [{name: s, http: {}}], finally: [{name: s, http: {}}]}]", `step "s": duplicate`},
		"dup labor":           {"targets: [{name: dev}]\nlabors: [{name: l, steps: [{name: s, http: {}}]}, {name: l, steps: [{name: s, http: {}}]}]", `labor "l": duplicate`},
		"dup target name":     {"targets: [{name: dev}, {name: dev}]\nlabors: [{name: l, steps: [{name: s, http: {}}]}]", `target "dev": duplicate`},
		"no var source":       {base + "vars: {x: {secret: true}}\n", "exactly one of value, file or env"},
		"bad var name":        {base + "vars: {my-key: {value: 1}}\n", "name must match"},
		"step name missing":   {"targets: [{name: dev}]\nlabors: [{name: l, steps: [{http: {}}]}]", "name is required"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestOnlyTarget(t *testing.T) {
	c, err := Load("testdata/valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.OnlyTarget("nope"); err == nil {
		t.Error("expected error for unknown target")
	}
	if err := c.OnlyTarget("prod"); err != nil || len(c.Targets) != 1 || c.Targets[0].Name != "prod" {
		t.Errorf("OnlyTarget(prod) = %v, targets %+v", err, c.Targets)
	}
}
