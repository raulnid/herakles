package httpstep

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/ohler55/ojg/jp"
)

const (
	excerptLen = 200
	// shortcut: JSON captures and asserts parse at most 1 MiB, raise it if an API returns bigger documents.
	maxJSON = 1 << 20
)

// respBody is the response read in one streaming pass: hashed and counted
// whole, with only a bounded prefix kept in memory.
type respBody struct {
	sha256 string
	size   int64
	head   []byte
}

// readBody hashes only when asked: sha256 of a large download costs CPU for nothing.
func readBody(r io.Reader, keepJSON, hash bool) (*respBody, error) {
	keep := excerptLen
	if keepJSON {
		keep = maxJSON + 1
	}
	p := &prefix{max: keep}
	var w io.Writer = p
	h := sha256.New()
	if hash {
		w = io.MultiWriter(h, p)
	}
	n, err := io.Copy(w, r)
	rb := &respBody{size: n, head: p.b}
	if hash {
		rb.sha256 = hex.EncodeToString(h.Sum(nil))
	}
	return rb, err
}

func (rb *respBody) json() (any, error) {
	if len(rb.head) > maxJSON {
		return nil, fmt.Errorf("response body over %d bytes, too large for jsonpath", maxJSON)
	}
	// UseNumber keeps large integer ids exact; jp walks the generic result.
	dec := json.NewDecoder(bytes.NewReader(rb.head))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("response is not JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("response is not JSON: data after the first value")
	}
	return doc, nil
}

// excerpt is the first bytes of the body for error messages, on one line.
func (rb *respBody) excerpt() string {
	b := rb.head[:min(len(rb.head), excerptLen)]
	if len(b) == 0 {
		return ""
	}
	s := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, string(b))
	return "; body: " + strings.TrimSpace(strings.ToValidUTF8(s, "?"))
}

type prefix struct {
	max int
	b   []byte
}

func (p *prefix) Write(b []byte) (int, error) {
	if room := p.max - len(p.b); room > 0 {
		p.b = append(p.b, b[:min(room, len(b))]...)
	}
	return len(b), nil
}

// paths caches compiled JSONPath expressions; like templates, they come from
// the config and never change.
var paths sync.Map

func compile(path string) (jp.Expr, error) {
	if x, ok := paths.Load(path); ok {
		return x.(jp.Expr), nil
	}
	x, err := jp.ParseString(path)
	if err != nil {
		return nil, err
	}
	paths.Store(path, x)
	return x, nil
}

func first(doc any, path string) (any, error) {
	x, err := compile(path)
	if err != nil {
		return nil, fmt.Errorf("jsonpath %s: %w", path, err)
	}
	got := x.Get(doc)
	if len(got) == 0 {
		return nil, errors.New("jsonpath " + path + " matched nothing")
	}
	return got[0], nil
}

// jsonEqual compares through JSON so 5, int64(5) and 5.0 are equal.
func jsonEqual(a, b any) bool { return jsonString(a) == jsonString(b) }

func jsonString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// parseDigest reads a SHA-256 digest sent by the server: plain hex, plain
// base64 (as in x-amz-checksum-sha256), or the sha-256 member of an RFC 9530
// Content-Digest / Repr-Digest or an RFC 3230 Digest header. It returns hex.
func parseDigest(v string) (string, error) {
	for _, member := range strings.Split(v, ",") {
		if k, val, ok := strings.Cut(strings.TrimSpace(member), "="); ok && strings.EqualFold(k, "sha-256") {
			v = strings.Trim(val, ":")
			break
		}
	}
	v = strings.TrimSpace(v)
	if b, err := hex.DecodeString(v); err == nil && len(b) == sha256.Size {
		return strings.ToLower(v), nil
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == sha256.Size {
		return hex.EncodeToString(b), nil
	}
	return "", fmt.Errorf("not a SHA-256 digest: %q", v)
}
