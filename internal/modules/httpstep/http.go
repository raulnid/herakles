// Package httpstep is the http module: one HTTP request with captures,
// assertions, per-phase timings and transfer rates.
package httpstep

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptrace"
	"slices"
	"strings"
	"time"

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
	Size     *int64         `yaml:"size"`     // response body length in bytes
	// The body's SHA-256 is checked against SHA256 (hex, usually the
	// {{ .vars.upload.sha256 }} of a payload or a captured value) and/or
	// against the digest the server sends in the SHA256Header response
	// header (hex, base64 or RFC 9530 Content-Digest).
	SHA256       string `yaml:"sha256"`
	SHA256Header string `yaml:"sha256_header"`
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

func (c *Config) downloads() bool { return c.hashes() || c.Assert.Size != nil }

func (c *Config) hashes() bool { return c.Assert.SHA256 != "" || c.Assert.SHA256Header != "" }

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
		if cp.JSONPath != "" && !module.Templated(cp.JSONPath) {
			if _, err := compile(cp.JSONPath); err != nil {
				errs = append(errs, fmt.Errorf("capture %q: jsonpath: %w", name, err))
			}
		}
	}
	if s := c.Assert.SHA256; s != "" && !module.Templated(s) {
		if b, err := hex.DecodeString(s); err != nil || len(b) != sha256.Size {
			errs = append(errs, fmt.Errorf("assert sha256 %q: want 64 hex characters", s))
		}
	}
	for path := range c.Assert.JSONPath {
		if module.Templated(path) {
			continue
		}
		if _, err := compile(path); err != nil {
			errs = append(errs, fmt.Errorf("assert jsonpath %q: %w", path, err))
		}
	}
	for _, s := range c.Assert.Status {
		if s != 0 && (s < 100 || s > 599) { // 0: templated, unknown until run time
			errs = append(errs, fmt.Errorf("assert status %d: not an HTTP status code", s))
		}
	}
	return errors.Join(errs...)
}

// Run does not repeat Validate: the config was checked at startup, and a bad
// templated value fails where it is used.
func (Module) Run(ctx context.Context, cfg any, env *module.Env) module.Result {
	c := cfg.(*Config)
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
	resp, err := withSafeRedirects(env).Do(req)
	if err != nil {
		return module.Result{Err: err, Phases: tr.phases(time.Now())}
	}
	defer resp.Body.Close()
	rb, readErr := readBody(resp.Body, c.needsJSON(), c.hashes())
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
	if h := c.Assert.SHA256Header; h != "" {
		v := resp.Header.Get(h)
		if v == "" {
			return nil, fmt.Errorf("assert sha256_header: header %s not in response", h)
		}
		want, err := parseDigest(v)
		if err != nil {
			return nil, fmt.Errorf("assert sha256_header %s: %w", h, err)
		}
		if rb.sha256 != want {
			return nil, fmt.Errorf("body sha256 %s, but header %s says %s", rb.sha256, h, want)
		}
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

// withSafeRedirects shares the cycle's transport and cookies but, on a
// redirect to another host, drops every header that carries a secret. Go
// itself only drops Authorization and Cookie, so a custom X-Api-Key header
// would otherwise follow the redirect.
func withSafeRedirects(env *module.Env) *http.Client {
	c := *env.HTTPClient
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if env.Vars == nil || req.URL.Host == via[0].URL.Host {
			return nil
		}
		for k, vs := range req.Header {
			for _, v := range vs {
				if env.Vars.Redact(v) != v {
					req.Header.Del(k)
					break
				}
			}
		}
		return nil
	}
	return &c
}
