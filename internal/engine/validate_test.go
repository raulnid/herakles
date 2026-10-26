package engine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/raulnid/herakles/internal/config"
	_ "github.com/raulnid/herakles/internal/modules/httpstep"
)

func TestValidate(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"valid": {`
vars: {base: {value: x}}
targets: [{name: dev}]
labors:
  - name: login
    steps:
      - {name: a, fake: {set: {token: t}, echo: "{{ .vars.base }} {{ .target }} {{ .cycle.id }}"}}
  - name: use
    steps: [{name: b, fake: {echo: "{{ .vars.token }}"}}]
    finally: [{name: c, fake: {echo: "{{ .vars.late }}"}}]
  - name: later
    steps: [{name: d, fake: {set: {late: x}}}]
  - name: logout
    finalizer: true
    steps: [{name: e, fake: {echo: "{{ .vars.late }}"}}]
`, ""},
		"undefined var": {`
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, fake: {echo: "{{ .vars.ghost }}"}}]}]
`, `labor "l" step "s": variable "ghost" is not declared`},
		"used before set": {`
targets: [{name: dev}]
labors:
  - name: l
    steps:
      - {name: a, fake: {echo: "{{ .vars.token }}"}}
      - {name: b, fake: {set: {token: t}}}
`, `step "a": variable "token"`},
		"finally typo": {`
targets: [{name: dev}]
labors: [{name: l, steps: [{name: a, fake: {}}], finally: [{name: c, fake: {echo: "{{ .vars.tokn }}"}}]}]
`, `step "c": variable "tokn"`},
		"var missing in one target": {`
targets: [{name: dev, vars: {key: {value: k}}}, {name: prod}]
labors: [{name: l, steps: [{name: s, fake: {echo: "{{ .vars.key }}"}}]}]
`, `variable "key" is not declared in vars nor set by an earlier step (target prod)`},
		"malformed template": {`
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, fake: {echo: "{{ .vars.x "}}]}]
`, `step "s": template:`},
		"unknown module": {`
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, grpc: {}}]}]
`, `unknown module "grpc" (available: `},
		"unknown module field": {`
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, fake: {ech: x}}]}]
`, `fake: yaml: unmarshal errors`},
		"bad output name": {`
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, fake: {set: {my-id: x}}}]}]
`, `output "my-id": name must match`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateTemplatedFieldsDecode(t *testing.T) {
	// A templated duration must not fail static decoding.
	cfg, err := config.Parse([]byte(`
vars: {d: {value: 1s}}
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, fake: {sleep: "{{ .vars.d }}"}}]}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

// Templated values are unknown during validation: they must not be reported
// as missing, malformed or duplicated.
func TestValidateTemplatedHTTPFields(t *testing.T) {
	cfg, err := config.Parse([]byte(`
vars: {id: {value: "42"}, code: {value: 200}, sum: {value: x}, kind: {value: zip}, size: {value: 10}, path: {value: $.a}}
targets: [{name: dev}]
labors:
  - name: l
    steps:
      - name: s
        http:
          url: "http://x/{{ .vars.id | pathescape }}"
          body: {payload: {kind: "{{ .vars.kind }}", size: "{{ .vars.size }}"}}
          capture:
            n: {jsonpath: "$.items[?(@.id=='{{ .vars.id }}')].name"}
            m: {jsonpath: "{{ .vars.path }}"}
          assert:
            status: ["{{ .vars.code }}", 201]
            sha256: "{{ .vars.sum }}"
            jsonpath:
              "$.items[?(@.id=='{{ .vars.id }}')].name": a
              "$.other[?(@.id=='{{ .vars.id }}')].name": b
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}

	// Real mistakes next to templates are still reported.
	cfg, err = config.Parse([]byte(`
targets: [{name: dev}]
labors: [{name: l, steps: [{name: s, http: {url: "{{ .target }}", assert: {status: [700], sha256: nothex}, capture: {c: {jsonpath: "$[?"}}, bogus: 1}}]}]
`))
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "field bogus not found") {
		t.Errorf("unknown field next to a template: %v", err)
	}
}

// A value captured from a server is data: template syntax in it is never run.
func TestCapturedValuesAreNotTemplates(t *testing.T) {
	e, _ := setup(t, `
vars: {key: {value: s3cr3t-key}}
targets: [{name: dev}]
labors:
  - name: l
    steps:
      # The config renders to the literal text {{ .vars.key }}, as if a server returned it.
      - {name: a, fake: {set: {evil: '{{ "{{ .vars.key }}" }}'}}}
      - {name: b, fake: {echo: "got {{ .vars.evil }}"}}
`)
	e.Once(context.Background())
	if got := ranSteps(); !slices.Equal(got, []string{"got {{ .vars.key }}"}) {
		t.Errorf("ran %v: a captured template was executed", got)
	}
}
