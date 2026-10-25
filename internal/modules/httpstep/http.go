// Package httpstep is the http module: one HTTP request with captures,
// assertions, per-phase timings and transfer rates.
package httpstep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptrace"
	"slices"
	"strings"
	"time"

	"github.com/ohler55/ojg/jp"

	"github.com/raulnid/herakles/internal/module"
	"github.com/raulnid/herakles/internal/vars"
)

func init() { module.Register("http", Module{}) }

type Config struct {
	Method  string             `yaml:"method"`
	URL     string             `yaml:"url"`
	Headers map[string]string  `yaml:"headers"`
	Body    *Body              `yaml:"body"`
	Capture map[string]Capture `yaml:"capture"`
	Assert  Assert             `yaml:"assert"`
}

// Capture stores one value of the response in a variable. Exactly one of
// JSONPath, Header or Status is set.
type Capture struct {
	JSONPath string `yaml:"jsonpath"`
	Header   string `yaml:"header"`
	Status   bool   `yaml:"status"`
	Secret   bool   `yaml:"secret"`
}

type Assert struct {
	// Status lists accepted codes. Empty means any 2xx. Use it for expected
	// failures too: [401, 403] checks that a request is rejected.
	Status   []int          `yaml:"status"`
	JSONPath map[string]any `yaml:"jsonpath"` // path -> expected value
	SHA256   string         `yaml:"sha256"`   // hex digest of the response body
	Size     *int64         `yaml:"size"`     // response body length in bytes
}

type Module struct{}

func (Module) NewConfig() any { return &Config{} }

func (Module) Outputs(cfg any) []string {
	c := cfg.(*Config)
	out := make([]string, 0, len(c.Capture))
	for name := range c.Capture {
		out = append(out, name)
	}
	return append(out, c.Body.outputNames()...)
}

func (Module) IsTransfer(cfg any) bool {
	c := cfg.(*Config)
	return c.downloads() || c.Body.uploads()
}

func (c *Config) downloads() bool { return c.Assert.SHA256 != "" || c.Assert.Size != nil }

func (Module) Validate(cfg any) error {
	c := cfg.(*Config)
	var errs []error
	if err := c.Body.validate(); err != nil {
		errs = append(errs, err)
	}
	for name, cp := range c.Capture {
		n := 0
		for _, set := range []bool{cp.JSONPath != "", cp.Header != "", cp.Status} {
			if set {
				n++
			}
		}
		if n != 1 {
			errs = append(errs, fmt.Errorf("capture %q: exactly one of jsonpath, header or status is required", name))
		}
		if cp.JSONPath != "" {
			if _, err := jp.ParseString(cp.JSONPath); err != nil {
				errs = append(errs, fmt.Errorf("capture %q: jsonpath: %w", name, err))
			}
		}
	}
	for path := range c.Assert.JSONPath {
		if _, err := jp.ParseString(path); err != nil {
			errs = append(errs, fmt.Errorf("assert jsonpath %q: %w", path, err))
		}
	}
	for _, s := range c.Assert.Status {
		if s < 100 || s > 599 {
			errs = append(errs, fmt.Errorf("assert status %d: not an HTTP status code", s))
		}
	}
	return errors.Join(errs...)
}

func (m Module) Run(ctx context.Context, cfg any, env *module.Env) module.Result {
	c := cfg.(*Config)
	if err := m.Validate(c); err != nil {
		return module.Result{Err: err}
	}
	body, err := c.Body.build()
	if err != nil {
		return module.Result{Err: err}
	}
	var reqBody io.Reader
	if body.r != nil {
		reqBody = body
	}
	tr := &tracer{}
	method := orDefault(strings.ToUpper(c.Method), http.MethodGet)
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, tr.clientTrace()), method, c.URL, reqBody)
	if err != nil {
		_ = body.Close()
		return module.Result{Err: err}
	}
	req.ContentLength = body.length
	req.Header.Set("User-Agent", env.UserAgent)
	if body.ctype != "" {
		req.Header.Set("Content-Type", body.ctype)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}

	tr.begin()
	resp, err := env.HTTPClient.Do(req)
	if err != nil {
		return module.Result{Err: err, Phases: tr.phases(time.Now())}
	}
	defer resp.Body.Close()
	rb, readErr := readBody(resp.Body, c.needsJSON(), c.Assert.SHA256 != "")
	res := module.Result{Phases: tr.phases(time.Now())}
	if body.uploads {
		n, firstRead := body.stats()
		res.Transfers = append(res.Transfers, module.Transfer{Direction: "upload", Bytes: n, Duration: tr.uploadTime(firstRead)})
	}
	if c.downloads() {
		res.Transfers = append(res.Transfers, module.Transfer{Direction: "download", Bytes: rb.size, Duration: res.Phases["transfer"]})
	}
	if readErr != nil {
		res.Err = fmt.Errorf("reading response body: %w", readErr)
		return res
	}
	if res.Captured, res.Err = c.check(resp, rb); res.Err != nil {
		return res
	}
	outs, err := body.outputs()
	if err != nil {
		res.Err = err
		return res
	}
	maps.Copy(res.Captured, outs)
	return res
}

func (c *Config) needsJSON() bool {
	if len(c.Assert.JSONPath) > 0 {
		return true
	}
	for _, cp := range c.Capture {
		if cp.JSONPath != "" {
			return true
		}
	}
	return false
}

// check applies assertions in order and then captures.
func (c *Config) check(resp *http.Response, rb *respBody) (map[string]vars.Value, error) {
	if ok := slices.Contains(c.Assert.Status, resp.StatusCode); !ok && (len(c.Assert.Status) > 0 || resp.StatusCode/100 != 2) {
		want := "2xx"
		if len(c.Assert.Status) > 0 {
			want = fmt.Sprint(c.Assert.Status)
		}
		return nil, fmt.Errorf("status %d, want %s%s", resp.StatusCode, want, rb.excerpt())
	}
	if c.Assert.Size != nil && rb.size != *c.Assert.Size {
		return nil, fmt.Errorf("body size %d bytes, want %d", rb.size, *c.Assert.Size)
	}
	if c.Assert.SHA256 != "" && !strings.EqualFold(rb.sha256, c.Assert.SHA256) {
		return nil, fmt.Errorf("body sha256 %s, want %s", rb.sha256, strings.ToLower(c.Assert.SHA256))
	}

	var doc any
	if c.needsJSON() {
		var err error
		if doc, err = rb.json(); err != nil {
			return nil, fmt.Errorf("%w%s", err, rb.excerpt())
		}
	}
	for _, path := range sortedKeys(c.Assert.JSONPath) {
		got, err := first(doc, path)
		if err != nil {
			return nil, fmt.Errorf("assert %w%s", err, rb.excerpt())
		}
		if !jsonEqual(got, c.Assert.JSONPath[path]) {
			return nil, fmt.Errorf("assert jsonpath %s = %s, want %s%s", path, jsonString(got), jsonString(c.Assert.JSONPath[path]), rb.excerpt())
		}
	}

	captured := map[string]vars.Value{}
	for _, name := range sortedKeys(c.Capture) {
		cp := c.Capture[name]
		var raw any
		switch {
		case cp.Status:
			raw = resp.StatusCode
		case cp.Header != "":
			if raw = resp.Header.Get(cp.Header); raw == "" {
				return nil, fmt.Errorf("capture %q: header %s not in response", name, cp.Header)
			}
		default:
			v, err := first(doc, cp.JSONPath)
			if err != nil {
				return nil, fmt.Errorf("capture %q: %w%s", name, err, rb.excerpt())
			}
			raw = v
		}
		captured[name] = vars.Value{Raw: raw, Secret: cp.Secret}
	}
	return captured, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
