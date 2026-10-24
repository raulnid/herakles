package tmpl

import (
	"fmt"
	"strings"
	"text/template"
	"text/template/parse"

	"go.yaml.in/yaml/v3"
)

// UndefinedError reports a template that reads a variable no step has set.
type UndefinedError struct{ Name string }

func (e *UndefinedError) Error() string { return fmt.Sprintf("undefined variable %q", e.Name) }

// Render returns a copy of n with every templated string scalar executed
// against data. n is not modified.
//
// A scalar that is exactly one variable, like "{{ .vars.code }}", keeps the
// variable's type: an int can fill an int field or stay a number in a JSON
// body. Anything else renders to a string.
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
		s.Value = b.String()
		if v, ok := soleValue(t, data); ok {
			if _, isStr := v.(string); !isStr {
				s.Tag, s.Style = "", 0 // let YAML resolve the number or bool again
			}
		}
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

// soleValue returns the value of a template made of a single field such as
// {{ .vars.a.b }}, looked up through nested maps.
func soleValue(t *template.Template, data map[string]any) (any, bool) {
	root := t.Tree.Root
	if len(root.Nodes) != 1 {
		return nil, false
	}
	a, ok := root.Nodes[0].(*parse.ActionNode)
	if !ok || len(a.Pipe.Decl) > 0 || len(a.Pipe.Cmds) != 1 || len(a.Pipe.Cmds[0].Args) != 1 {
		return nil, false
	}
	f, ok := a.Pipe.Cmds[0].Args[0].(*parse.FieldNode)
	if !ok {
		return nil, false
	}
	var cur any = data
	for _, id := range f.Ident {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[id]; !ok {
			return nil, false
		}
	}
	return cur, true
}
