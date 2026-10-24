package tmpl

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// UndefinedError reports a template that reads a variable no step has set.
type UndefinedError struct{ Name string }

func (e *UndefinedError) Error() string { return fmt.Sprintf("undefined variable %q", e.Name) }

// Render returns a copy of n with every templated string scalar executed
// against data. Rendered scalars lose their string tag, so YAML resolves them
// again on decode: "{{ .vars.code }}" can fill an int field. n is not modified.
func Render(n *yaml.Node, data map[string]any) (*yaml.Node, error) {
	out := clone(n)
	vars, _ := data["vars"].(map[string]any)
	err := eachTemplate(out, func(s *yaml.Node) error {
		t, err := parseTemplate(s.Value)
		if err != nil {
			return err
		}
		for _, name := range refsOf(t) {
			if _, ok := vars[name]; !ok {
				return &UndefinedError{Name: name}
			}
		}
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return err
		}
		s.Value, s.Tag, s.Style = b.String(), "", 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// clone deep-copies n, inlining aliases so they render too.
func clone(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		return clone(n.Alias)
	}
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		c.Content[i] = clone(ch)
	}
	return &c
}

// Static returns a copy of n with every templated scalar set to null, so a
// config can be decoded and checked before any variable exists.
func Static(n *yaml.Node) *yaml.Node {
	out := clone(n)
	_ = eachTemplate(out, func(s *yaml.Node) error {
		s.Value, s.Tag, s.Style = "", "!!null", 0
		return nil
	})
	return out
}
