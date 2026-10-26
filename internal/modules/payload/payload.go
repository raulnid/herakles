// Package payload generates synthetic files in memory, streamed and hashed on
// the fly, so an upload can later be checked against its download.
//
// New kinds register a Kind from init:
//
//	func init() { payload.Register("pdf", payload.Kind{ContentType: "application/pdf", Ext: "pdf", Write: writePDF}) }
package payload

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	mrand "math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

// Kind writes about size bytes of incompressible content to w, reading
// randomness from rng.
type Kind struct {
	ContentType string
	Ext         string
	Write       func(w io.Writer, size int64, rng io.Reader) error
}

var (
	mu    sync.RWMutex
	kinds = map[string]Kind{}
)

func Register(name string, k Kind) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := kinds[name]; dup {
		panic(fmt.Sprintf("payload kind %q registered twice", name))
	}
	kinds[name] = k
}

func lookup(name string) (Kind, bool) {
	mu.RLock()
	defer mu.RUnlock()
	k, ok := kinds[name]
	return k, ok
}

// MaxSize bounds payloads: a probe never needs more, and it keeps PNG
// dimensions within their 32-bit fields.
const MaxSize = 4 << 30

// Spec is the YAML form: {kind: zip, size: 5MiB, as: upload}.
type Spec struct {
	Kind string `yaml:"kind"`
	Size Size   `yaml:"size"`
	// As names the variable that receives {sha256, size} after the upload.
	As string `yaml:"as"`
}

// Known reports whether kind is registered.
func Known(kind string) bool { _, ok := lookup(kind); return ok }

func (s *Spec) Validate() error {
	if _, ok := lookup(s.Kind); !ok {
		return fmt.Errorf("payload: unknown kind %q", s.Kind)
	}
	if s.Size <= 0 || s.Size > MaxSize {
		return fmt.Errorf("payload: size must be between 1 byte and 4GiB")
	}
	return nil
}

func (s *Spec) ContentType() string { k, _ := lookup(s.Kind); return k.ContentType }

func (s *Spec) Filename() string { k, _ := lookup(s.Kind); return "payload." + k.Ext }

// Open starts generating the payload. The stream must be read to EOF (or
// closed) to release the generator goroutine.
func (s *Spec) Open() (*Stream, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	k, _ := lookup(s.Kind)
	var seed [32]byte
	_, _ = rand.Read(seed[:]) // never fails
	rng := mrand.NewChaCha8(seed)

	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(k.Write(pw, int64(s.Size), rng)) }()
	return &Stream{r: pr, h: sha256.New()}, nil
}

// Stream hashes and counts the bytes as they are read.
type Stream struct {
	r *io.PipeReader

	mu  sync.Mutex
	h   hash.Hash
	n   int64
	eof bool
}

func (st *Stream) Read(p []byte) (int, error) {
	n, err := st.r.Read(p)
	st.mu.Lock()
	st.h.Write(p[:n])
	st.n += int64(n)
	st.eof = st.eof || errors.Is(err, io.EOF)
	st.mu.Unlock()
	return n, err
}

func (st *Stream) Close() error { return st.r.Close() }

// Sum is the hex sha256 and size of the payload; complete is false until the
// stream has been read to EOF.
func (st *Stream) Sum() (sum string, size int64, complete bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return hex.EncodeToString(st.h.Sum(nil)), st.n, st.eof
}

// Size is a byte count written as 1024, 10KB or 5MiB.
type Size int64

var sizeRe = regexp.MustCompile(`^(\d+)\s*([KMG]i?B|B)?$`)

func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	if strings.Contains(n.Value, "{{") {
		return nil // unrendered, during validation: the rendered value is checked at run time
	}
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(n.Value))
	if m == nil {
		return fmt.Errorf("line %d: invalid size %q, want e.g. 512, 10KB or 5MiB", n.Line, n.Value)
	}
	v, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return err
	}
	mult := map[string]int64{"": 1, "B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30}[m[2]]
	*s = Size(v * mult)
	return nil
}
