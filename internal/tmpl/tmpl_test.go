package tmpl

import (
	"errors"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

func node(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

type cfg struct {
	URL     string            `yaml:"url"`
	Status  int               `yaml:"status"`
	Size    int64             `yaml:"size"`
	Code    string            `yaml:"code"`
	Headers map[string]string `yaml:"headers"`
}

func data(vars map[string]any) map[string]any {
	return map[string]any{"vars": vars, "target": "dev"}
}

func TestRenderTypesAndCopy(t *testing.T) {
	n := node(t, `
url: "{{ .vars.base }}/p/{{ .vars.id }}?t={{ .target }}"
status: "{{ .vars.status }}"
size: "{{ .vars.upload.size }}"
code: "{{ .vars.id }}"
headers: {Authorization: "Bearer {{ .vars.token }}", X-Plain: "200"}
`)
	out, err := Render(n, data(map[string]any{
		"base": "http://h", "id": 42, "status": 201, "token": "abc",
		"upload": map[string]any{"size": int64(5 << 20)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	var got cfg
	if err := out.Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := cfg{URL: "http://h/p/42?t=dev", Status: 201, Size: 5 << 20, Code: "42",
		Headers: map[string]string{"Authorization": "Bearer abc", "X-Plain": "200"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if n.Content[1].Value != "{{ .vars.base }}/p/{{ .vars.id }}?t={{ .target }}" {
		t.Error("Render modified its input")
	}
}

func TestRenderKeepsVariableTypes(t *testing.T) {
	n := node(t, `{key: "{{ .vars.key }}", n: "{{ .vars.n }}", on: "{{ .vars.on }}", mixed: "n={{ .vars.n }}", lit: "{{ .vars.null }}"}`)
	out, err := Render(n, data(map[string]any{"key": "0123", "n": 5, "on": true, "null": "null"}))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := out.Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"key": "0123", "n": 5, "on": true, "mixed": "n=5", "lit": "null"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

func TestRenderAlias(t *testing.T) {
	doc := node(t, `
common: &auth {Authorization: "Bearer {{ .vars.token }}"}
step: {headers: *auth}
`)
	out, err := Render(doc.Content[3], data(map[string]any{"token": "abc"}))
	if err != nil {
		t.Fatal(err)
	}
	var got cfg
	if err := out.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Headers["Authorization"] != "Bearer abc" {
		t.Errorf("alias not rendered: %+v", got)
	}
}

func TestRenderErrors(t *testing.T) {
	vars := map[string]any{"upload": map[string]any{"size": 1}}

	_, err := Render(node(t, `url: "{{ .vars.missing }}"`), data(vars))
	var undef *UndefinedError
	if !errors.As(err, &undef) || undef.Name != "missing" {
		t.Errorf("undefined var: err = %v", err)
	}

	if _, err := Render(node(t, `size: "{{ .vars.upload.sha256 }}"`), data(vars)); err == nil {
		t.Error("missing nested key: expected error")
	}

	if _, err := Render(node(t, `url: "{{ .vars.x "`), data(vars)); err == nil {
		t.Error("malformed template: expected error")
	}
}

func TestRefs(t *testing.T) {
	cases := map[string][]string{
		"plain text":                    nil,
		"{{ .vars.a }}-{{ .vars.b.c }}": {"a", "b"},
		"{{ $.vars.a }}{{ .vars.a }}":   {"a"},
		"{{ if .vars.a }}{{ .vars.b | printf \"%s\" }}{{ else }}{{ .vars.c }}{{ end }}": {"a", "b", "c"},
		"{{ .target }} {{ .cycle.id }}": nil,
	}
	for src, want := range cases {
		got, err := Refs(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Refs(%q) = %v, want %v", src, got, want)
		}
	}
	if _, err := Refs("{{ .vars.a"); err == nil {
		t.Error("malformed template: expected error")
	}
}

func TestNodeRefs(t *testing.T) {
	got, err := NodeRefs(node(t, `{url: "{{ .vars.base }}/x", body: {json: {k: "{{ .vars.key }}", n: 3, again: "{{ .vars.base }}"}}}`))
	if err != nil || !reflect.DeepEqual(got, []string{"base", "key"}) {
		t.Errorf("NodeRefs = %v, %v", got, err)
	}
}
