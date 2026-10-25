package httpstep

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"
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
	doc, err := oj.Parse(rb.head)
	if err != nil {
		return nil, fmt.Errorf("response is not JSON: %w", err)
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

func first(doc any, path string) (any, error) {
	x, err := jp.ParseString(path)
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
