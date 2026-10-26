// Package tmpl renders text/template expressions inside step configs.
package tmpl

import (
	"net/url"
	"slices"
	"strings"
	"sync"
	"text/template"
	"text/template/parse"

	"go.yaml.in/yaml/v3"
)

// funcs adds pathescape for values placed in a URL path; text/template has
// urlquery built in for query strings.
var funcs = template.FuncMap{"pathescape": url.PathEscape}

// parsed caches templates by source: configs never change at runtime, so
// each template is parsed once. Parsed templates are safe for concurrent use.
var parsed sync.Map

func parseTemplate(src string) (*template.Template, error) {
	if t, ok := parsed.Load(src); ok {
		return t.(*template.Template), nil
	}
	t, err := template.New("").Option("missingkey=error").Funcs(funcs).Parse(src)
	if err != nil {
		return nil, err
	}
	parsed.Store(src, t)
	return t, nil
}

// Refs returns the top-level variable names a template reads as .vars.NAME
// or $.vars.NAME, in order of first use.
//
// shortcut: inside with/range the dot changes and .vars.X is still reported;
// upgrade if someone rebinds dot to a map that has its own vars key.
func Refs(src string) ([]string, error) {
	t, err := parseTemplate(src)
	if err != nil {
		return nil, err
	}
	return refsOf(t), nil
}

func refsOf(t *template.Template) []string {
	var out []string
	seen := map[string]bool{}
	add := func(ident []string) {
		if len(ident) >= 2 && ident[0] == "vars" && !seen[ident[1]] {
			seen[ident[1]] = true
			out = append(out, ident[1])
		}
	}
	var walk func(parse.Node)
	walk = func(n parse.Node) {
		switch n := n.(type) {
		case *parse.ListNode:
			if n != nil {
				for _, c := range n.Nodes {
					walk(c)
				}
			}
		case *parse.ActionNode:
			walk(n.Pipe)
		case *parse.PipeNode:
			if n != nil {
				for _, c := range n.Cmds {
					walk(c)
				}
			}
		case *parse.CommandNode:
			for _, a := range n.Args {
				walk(a)
			}
		case *parse.FieldNode:
			add(n.Ident)
		case *parse.VariableNode:
			if len(n.Ident) > 0 && n.Ident[0] == "$" {
				add(n.Ident[1:])
			}
		case *parse.ChainNode:
			walk(n.Node)
		case *parse.IfNode:
			walkBranch(&n.BranchNode, walk)
		case *parse.WithNode:
			walkBranch(&n.BranchNode, walk)
		case *parse.RangeNode:
			walkBranch(&n.BranchNode, walk)
		}
	}
	if t.Tree != nil { //nolint:staticcheck // nil check needs the field
		walk(t.Root)
	}
	return out
}

func walkBranch(b *parse.BranchNode, walk func(parse.Node)) {
	walk(b.Pipe)
	walk(b.List)
	walk(b.ElseList)
}

// NodeRefs returns the variable names referenced anywhere under n, once each
// in order of first use, or the first malformed template.
func NodeRefs(n *yaml.Node) ([]string, error) {
	var out []string
	err := eachTemplate(n, func(s *yaml.Node) error {
		r, err := Refs(s.Value)
		for _, name := range r {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		return err
	})
	return out, err
}

// eachTemplate calls fn for every string scalar under n that holds a template.
func eachTemplate(n *yaml.Node, fn func(*yaml.Node) error) error {
	switch n.Kind {
	case yaml.AliasNode:
		return eachTemplate(n.Alias, fn)
	case yaml.ScalarNode:
		if n.ShortTag() == "!!str" && strings.Contains(n.Value, "{{") {
			return fn(n)
		}
		return nil
	}
	for _, c := range n.Content {
		if err := eachTemplate(c, fn); err != nil {
			return err
		}
	}
	return nil
}
