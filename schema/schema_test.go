// Package schema holds the JSON Schema of the config file; this test keeps it
// in sync with the examples.
package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

func compile(t *testing.T) *jsonschema.Schema {
	t.Helper()
	s, err := jsonschema.NewCompiler().Compile("herakles.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(s *jsonschema.Schema, src []byte) error {
	var doc any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return err
	}
	j, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(j))
	if err != nil {
		return err
	}
	return s.Validate(inst)
}

func TestExamplesMatchSchema(t *testing.T) {
	s := compile(t)
	files, _ := filepath.Glob("../examples/*.yaml")
	files = append(files, "../internal/config/testdata/valid.yaml")
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := validate(s, src); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestSchemaRejects(t *testing.T) {
	s := compile(t)
	base := "targets: [{name: dev}]\nlabors: [{name: l, steps: [STEP]}]\n"
	cases := map[string]string{
		"unknown top field": "bogus: 1\n" + strings.Replace(base, "STEP", "{name: s, http: {url: x}}", 1),
		"no module":         strings.Replace(base, "STEP", "{name: s}", 1),
		"unknown module":    strings.Replace(base, "STEP", "{name: s, grpc: {}}", 1),
		"two body kinds":    strings.Replace(base, "STEP", "{name: s, http: {body: {json: {}, text: x}}}", 1),
		"capture two ways":  strings.Replace(base, "STEP", "{name: s, http: {capture: {a: {jsonpath: $.a, header: X}}}}", 1),
		"bad status":        strings.Replace(base, "STEP", "{name: s, http: {assert: {status: [99]}}}", 1),
		"bad size":          strings.Replace(base, "STEP", "{name: s, http: {body: {payload: {kind: zip, size: 5 megs}}}}", 1),
		"var two sources":   "vars: {k: {value: 1, env: K}}\n" + strings.Replace(base, "STEP", "{name: s, http: {}}", 1),
		"bad duration":      "interval: soon\n" + strings.Replace(base, "STEP", "{name: s, http: {}}", 1),
	}
	for name, src := range cases {
		if err := validate(s, []byte(src)); err == nil {
			t.Errorf("%s: schema accepted invalid config", name)
		}
	}
	if err := validate(s, []byte(strings.Replace(base, "STEP", `{name: s, http: {assert: {status: ["{{ .vars.code }}"], size: "{{ .vars.up.size }}"}}}`, 1))); err != nil {
		t.Errorf("templated fields rejected: %v", err)
	}
}
