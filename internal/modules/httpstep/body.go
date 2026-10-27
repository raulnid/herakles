package httpstep

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/raulnid/herakles/internal/module"
	"github.com/raulnid/herakles/internal/modules/payload"
	"github.com/raulnid/herakles/internal/vars"
)

// Body is the request body. At most one field is set.
type Body struct {
	JSON      any             `yaml:"json"`
	Text      *string         `yaml:"text"`
	Multipart map[string]Part `yaml:"multipart"`
	Payload   *payload.Spec   `yaml:"payload"` // generated file sent as the raw body
}

// Part is one multipart field: a plain value, or a file when Filename or
// Payload is set.
type Part struct {
	Value       string        `yaml:"value"`
	Filename    string        `yaml:"filename"`
	ContentType string        `yaml:"content_type"`
	Payload     *payload.Spec `yaml:"payload"`
}

func (p *Part) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&p.Value)
	}
	type plain Part
	return n.Decode((*plain)(p))
}

func (b *Body) validate() error {
	if b == nil {
		return nil
	}
	n := 0
	for _, set := range []bool{b.JSON != nil, b.Text != nil, b.Multipart != nil, b.Payload != nil} {
		if set {
			n++
		}
	}
	var errs []error
	if n > 1 {
		errs = append(errs, errors.New("body: set only one of json, text, multipart or payload"))
	}
	for name, p := range b.Multipart {
		if p.Payload != nil && p.Value != "" {
			errs = append(errs, fmt.Errorf("body multipart %q: set value or payload, not both", name))
		}
	}
	for _, sp := range b.payloads() {
		if !module.Templated(sp.Kind) && !payload.Known(sp.Kind) {
			errs = append(errs, fmt.Errorf("body: unknown payload kind %q", sp.Kind))
		}
	}
	return errors.Join(errs...)
}

func (b *Body) payloads() []*payload.Spec {
	if b == nil {
		return nil
	}
	var out []*payload.Spec
	if b.Payload != nil {
		out = append(out, b.Payload)
	}
	for _, name := range slices.Sorted(maps.Keys(b.Multipart)) {
		if p := b.Multipart[name].Payload; p != nil {
			out = append(out, p)
		}
	}
	return out
}

func (b *Body) uploads() bool { return len(b.payloads()) > 0 }

// outputNames are the variables the body publishes: each payload's as.
func (b *Body) outputNames() []string {
	var out []string
	for _, sp := range b.payloads() {
		if sp.As != "" {
			out = append(out, sp.As)
		}
	}
	return out
}

// reqBody counts what the transport reads, for the upload rate. The
// transport reads it from its own goroutine, hence the mutex.
type reqBody struct {
	r       io.Reader
	length  int64 // -1 when unknown (streamed)
	ctype   string
	uploads bool
	streams map[*payload.Spec]*payload.Stream

	mu    sync.Mutex
	n     int64
	first time.Time
}

func (b *reqBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.mu.Lock()
	if b.first.IsZero() {
		b.first = time.Now()
	}
	b.n += int64(n)
	b.mu.Unlock()
	return n, err
}

func (b *reqBody) stats() (int64, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n, b.first
}

// Close stops streaming producers when the transport gives up early.
func (b *reqBody) Close() error {
	for _, st := range b.streams {
		_ = st.Close()
	}
	if c, ok := b.r.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// outputs returns {sha256, size} for every payload with an as name. It fails
// if the server answered before the payload was fully sent.
func (b *reqBody) outputs() (map[string]vars.Value, error) {
	out := map[string]vars.Value{}
	for sp, st := range b.streams {
		if sp.As == "" {
			continue
		}
		sum, size, complete := st.Sum()
		if !complete {
			return nil, fmt.Errorf("payload %q: upload incomplete, %d bytes sent", sp.As, size)
		}
		out[sp.As] = vars.Value{Raw: map[string]any{"sha256": sum, "size": size}}
	}
	return out, nil
}

func (b *Body) build() (*reqBody, error) {
	rb := &reqBody{streams: map[*payload.Spec]*payload.Stream{}}
	for _, sp := range b.payloads() {
		st, err := sp.Open()
		if err != nil {
			_ = rb.Close()
			return nil, err
		}
		rb.streams[sp] = st
	}
	switch {
	case b == nil:
	case b.JSON != nil:
		data, err := json.Marshal(b.JSON)
		if err != nil {
			return nil, fmt.Errorf("body json: %w", err)
		}
		rb.r, rb.length, rb.ctype = bytes.NewReader(data), int64(len(data)), "application/json"
	case b.Text != nil:
		rb.r, rb.length, rb.ctype = strings.NewReader(*b.Text), int64(len(*b.Text)), "text/plain; charset=utf-8"
	case b.Payload != nil:
		// shortcut: payloads are sent chunked, add Content-Length for exact-size kinds if a server rejects chunked uploads.
		rb.r, rb.length, rb.ctype, rb.uploads = rb.streams[b.Payload], -1, b.Payload.ContentType(), true
	case b.Multipart != nil:
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() { pw.CloseWithError(b.writeParts(mw, rb.streams)) }()
		rb.r, rb.length, rb.ctype, rb.uploads = pr, -1, mw.FormDataContentType(), b.uploads()
	}
	return rb, nil
}

// writeParts streams the parts in name order, so requests are reproducible.
func (b *Body) writeParts(mw *multipart.Writer, streams map[*payload.Spec]*payload.Stream) error {
	for _, name := range slices.Sorted(maps.Keys(b.Multipart)) {
		p := b.Multipart[name]
		filename, ctype := p.Filename, p.ContentType
		if p.Payload != nil {
			filename, ctype = cmp.Or(filename, p.Payload.Filename()), cmp.Or(ctype, p.Payload.ContentType())
		}
		if strings.ContainsAny(ctype, "\r\n") {
			return fmt.Errorf("multipart %q: content type contains a line break", name)
		}
		var w io.Writer
		var err error
		if filename == "" {
			w, err = mw.CreateFormField(name)
		} else {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, filename))
			h.Set("Content-Type", cmp.Or(ctype, "application/octet-stream"))
			w, err = mw.CreatePart(h)
		}
		if err != nil {
			return err
		}
		var src io.Reader = strings.NewReader(p.Value)
		if p.Payload != nil {
			src = streams[p.Payload]
		}
		if _, err := io.Copy(w, src); err != nil {
			return err
		}
	}
	return mw.Close()
}
