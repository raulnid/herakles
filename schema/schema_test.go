// Package schema holds the JSON Schema of the config file; this test keeps it
// in sync with the examples.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// stringKeys turns map[any]any, which YAML yields for keys such as 404, into
// JSON objects, as editors do before validating.
func stringKeys(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			v[k] = stringKeys(e)
		}
	case map[any]any:
		m := make(map[string]any, len(v))
		for k, e := range v {
			m[fmt.Sprint(k)] = stringKeys(e)
		}
		return m
	case []any:
		for i, e := range v {
			v[i] = stringKeys(e)
		}
	}
	return v
}

func validate(s *jsonschema.Schema, src []byte) error {
	var doc any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return err
	}
	j, err := json.Marshal(stringKeys(doc))
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
	files = append(files, "../internal/config/testdata/valid.yaml", "../examples/kubernetes/herakles.yaml")
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
		"unknown top field":  "bogus: 1\n" + strings.Replace(base, "STEP", "{name: s, http: {url: x}}", 1),
		"no module":          strings.Replace(base, "STEP", "{name: s}", 1),
		"unknown module":     strings.Replace(base, "STEP", "{name: s, grpc: {}}", 1),
		"two body kinds":     strings.Replace(base, "STEP", "{name: s, http: {body: {json: {}, text: x}}}", 1),
		"capture two ways":   strings.Replace(base, "STEP", "{name: s, http: {capture: {a: {jsonpath: $.a, header: X}}}}", 1),
		"bad status":         strings.Replace(base, "STEP", "{name: s, http: {assert: {status: [99]}}}", 1),
		"bad size":           strings.Replace(base, "STEP", "{name: s, http: {body: {payload: {kind: zip, size: 5 megs}}}}", 1),
		"var two sources":    "vars: {k: {value: 1, env: K}}\n" + strings.Replace(base, "STEP", "{name: s, http: {}}", 1),
		"bad duration":       "interval: soon\n" + strings.Replace(base, "STEP", "{name: s, http: {}}", 1),
		"tcp without addr":   strings.Replace(base, "STEP", "{name: s, tcp: {tls: true}}", 1),
		"tcp sni no tls":     strings.Replace(base, "STEP", "{name: s, tcp: {addr: \"h:1\", server_name: h}}", 1),
		"http and tcp":       strings.Replace(base, "STEP", "{name: s, http: {}, tcp: {addr: \"h:1\"}}", 1),
		"cert without key":   strings.Replace(strings.Replace(base, "STEP", "{name: s, http: {}}", 1), "{name: dev}", "{name: dev, tls: {cert_file: c.pem}}", 1),
		"on_status bad code": strings.Replace(base, "STEP", "{name: s, http: {assert: {on_status: {abc: {jsonpath: {$.a: 1}}}}}}", 1),
		"on_status size":     strings.Replace(base, "STEP", "{name: s, http: {assert: {on_status: {404: {size: 1}}}}}", 1),
		"foreach bad name":   strings.Replace(base, "STEP", "{name: s, foreach: \"a b\", http: {}}", 1),
		"all without path":   strings.Replace(base, "STEP", "{name: s, http: {capture: {a: {header: X, all: true}}}}", 1),
		"key without cert":   strings.Replace(strings.Replace(base, "STEP", "{name: s, http: {}}", 1), "{name: dev}", "{name: dev, tls: {key_file: k.pem}}", 1),
	}
	for name, src := range cases {
		if err := validate(s, []byte(src)); err == nil {
			t.Errorf("%s: schema accepted invalid config", name)
		}
	}
	if err := validate(s, []byte(strings.Replace(base, "STEP", `{name: s, http: {assert: {status: ["{{ .vars.code }}"], size: "{{ .vars.up.size }}"}}}`, 1))); err != nil {
		t.Errorf("templated fields rejected: %v", err)
	}
	if err := validate(s, []byte(strings.Replace(base, "STEP", "{name: s, http: {assert: {status: [200, 404], on_status: {404: {jsonpath: {$.m: x}}}}}}", 1))); err != nil {
		t.Errorf("on_status rejected: %v", err)
	}
	if err := validate(s, []byte(strings.Replace(base, "STEP", "{name: s, foreach: ids, http: {url: \"{{ .item }}\", capture: {ids: {jsonpath: $.a, all: true}}}}", 1))); err != nil {
		t.Errorf("foreach rejected: %v", err)
	}
	mtls := strings.Replace(strings.Replace(base, "STEP", "{name: s, http: {}}", 1), "{name: dev}", "{name: dev, tls: {ca_file: ca.pem, cert_file: c.pem, key_file: k.pem}}", 1)
	if err := validate(s, []byte(mtls)); err != nil {
		t.Errorf("mTLS target rejected: %v", err)
	}
}
