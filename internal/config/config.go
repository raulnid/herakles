// Package config defines the herakles YAML file and its structural validation.
package config

import (
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	DefaultInterval        = 60 * time.Second
	DefaultStepTimeout     = 10 * time.Second
	DefaultTransferTimeout = 120 * time.Second
	DefaultListen          = ":9090"
)

// DefaultUserAgent identifies the traffic as synthetic; main sets the version.
var DefaultUserAgent = "herakles/dev (synthetic probe)"

type Config struct {
	Interval        time.Duration  `yaml:"interval"`
	StepTimeout     time.Duration  `yaml:"step_timeout"`
	TransferTimeout time.Duration  `yaml:"transfer_timeout"`
	Listen          string         `yaml:"listen"`
	UserAgent       string         `yaml:"user_agent"`
	Vars            map[string]Var `yaml:"vars"`
	Targets         []Target       `yaml:"targets"`
	Labors          []Labor        `yaml:"labors"`
}

// Var is a variable source. Exactly one of Value, File or Env must be set.
type Var struct {
	Value  any    `yaml:"value"`
	File   string `yaml:"file"`
	Env    string `yaml:"env"`
	Secret bool   `yaml:"secret"`
}

type Target struct {
	Name string         `yaml:"name"`
	Safe bool           `yaml:"safe"`
	Vars map[string]Var `yaml:"vars"`
	TLS  TLS            `yaml:"tls"`
}

type TLS struct {
	CAFile             string `yaml:"ca_file"` // added to the system roots, re-read every cycle
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

type Labor struct {
	Name      string `yaml:"name"`
	Finalizer bool   `yaml:"finalizer"`
	Steps     []Step `yaml:"steps"`
	Finally   []Step `yaml:"finally"`
}

// Step holds the core fields; the single remaining key names the module and
// its raw config, decoded by the module after template rendering.
type Step struct {
	Name        string
	Description string // what the step does, shown when it starts
	Optional    bool
	Mutating    bool
	Module      string
	Config      yaml.Node
}

func (s *Step) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: step must be a mapping", n.Line)
	}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		var err error
		switch k.Value {
		case "name":
			err = v.Decode(&s.Name)
		case "description":
			err = v.Decode(&s.Description)
		case "optional":
			err = v.Decode(&s.Optional)
		case "mutating":
			err = v.Decode(&s.Mutating)
		default:
			if s.Module != "" {
				return fmt.Errorf("line %d: step has more than one module key (%q and %q)", k.Line, s.Module, k.Value)
			}
			s.Module, s.Config = k.Value, *v
		}
		if err != nil {
			return err
		}
	}
	if s.Module == "" {
		return fmt.Errorf("line %d: step %q has no module key", n.Line, s.Name)
	}
	return nil
}
