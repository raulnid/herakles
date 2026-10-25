// Package module is the contract between the herakles core and step modules.
//
// A module is compiled into the binary and registers itself from init:
//
//	func init() { module.Register("http", httpModule{}) }
//
// The core renders templates in the step config, decodes it into the value
// returned by NewConfig and calls Run with a context that carries the step
// timeout. Modules never see templates, metrics or logs.
package module

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/raulnid/herakles/internal/vars"
	"go.yaml.in/yaml/v3"
)

type Module interface {
	// NewConfig returns a pointer to a zero config struct with yaml tags.
	NewConfig() any
	Run(ctx context.Context, cfg any, env *Env) Result
}

// Optional interfaces a module may implement.
type (
	// Validator checks a config statically. Templated fields are zero values.
	Validator interface{ Validate(cfg any) error }
	// Outputs names the variables a step will set, so validate can check
	// references and the runner can skip steps whose inputs were skipped.
	Outputs interface{ Outputs(cfg any) []string }
	// BulkTransfer reports steps that move bulk data and get transfer_timeout.
	BulkTransfer interface{ IsTransfer(cfg any) bool }
)

// Env is what the core hands to a step. Vars is read-only for modules: new
// values go back through Result.Captured.
type Env struct {
	Target     string
	Vars       *vars.Vars
	HTTPClient *http.Client
	UserAgent  string
}

type Result struct {
	Err      error
	Captured map[string]vars.Value
	// Phases of the last network request, e.g. dns, connect, tls, wait, transfer, total.
	Phases    map[string]time.Duration
	Transfers []Transfer
}

type Transfer struct {
	Direction string // "upload" or "download"
	Bytes     int64
	Duration  time.Duration
}

func (t Transfer) BytesPerSecond() float64 {
	if t.Duration <= 0 {
		return 0
	}
	return float64(t.Bytes) / t.Duration.Seconds()
}

var (
	mu       sync.RWMutex
	registry = map[string]Module{}
)

// Register makes a module available under name. It panics on duplicates,
// which can only happen at init time.
func Register(name string, m Module) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("module %q registered twice", name))
	}
	registry[name] = m
}

func Get(name string) (Module, bool) {
	mu.RLock()
	defer mu.RUnlock()
	m, ok := registry[name]
	return m, ok
}

// Names lists registered modules, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Decode decodes a step config node into cfg, rejecting unknown fields.
func Decode(n *yaml.Node, cfg any) error {
	// yaml.Node.Decode has no strict mode, so round-trip through the encoder.
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
