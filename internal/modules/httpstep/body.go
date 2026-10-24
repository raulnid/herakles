package httpstep

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/raulnid/herakles/internal/vars"
)

// Body is the request body. At most one field is set.
type Body struct {
	JSON      any             `yaml:"json"`
	Text      *string         `yaml:"text"`
	Multipart map[string]Part `yaml:"multipart"`
}

// Part is one multipart field: a plain value, or a file when Filename is set.
type Part struct {
	Value       string `yaml:"value"`
	Filename    string `yaml:"filename"`
	ContentType string `yaml:"content_type"`
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
	for _, set := range []bool{b.JSON != nil, b.Text != nil, b.Multipart != nil} {
		if set {
			n++
		}
	}
	if n > 1 {
		return errors.New("body: set only one of json, text or multipart")
	}
	return nil
}

func (b *Body) uploads() bool { return false }

// outputNames and outputs are the variables the body publishes, such as a
// generated payload's checksum.
func (b *Body) outputNames() []string { return nil }

func (b *Body) outputs() map[string]vars.Value { return nil }

// reqBody counts what the transport reads, for the upload rate. The
// transport reads it from its own goroutine, hence the mutex.
type reqBody struct {
	r       io.Reader
	length  int64 // -1 when unknown (streamed)
	ctype   string
	uploads bool

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

// Close stops a streaming producer when the transport gives up early.
func (b *reqBody) Close() error {
	if c, ok := b.r.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (b *Body) build() (*reqBody, error) {
	switch {
	case b == nil:
		return &reqBody{}, nil
	case b.JSON != nil:
		data, err := json.Marshal(b.JSON)
		if err != nil {
			return nil, fmt.Errorf("body json: %w", err)
		}
		return &reqBody{r: bytes.NewReader(data), length: int64(len(data)), ctype: "application/json"}, nil
	case b.Text != nil:
		return &reqBody{r: strings.NewReader(*b.Text), length: int64(len(*b.Text)), ctype: "text/plain; charset=utf-8"}, nil
	case b.Multipart != nil:
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() { pw.CloseWithError(b.writeParts(mw)) }()
		return &reqBody{r: pr, length: -1, ctype: mw.FormDataContentType(), uploads: b.uploads()}, nil
	}
	return &reqBody{}, nil
}

// writeParts streams the parts in name order, so requests are reproducible.
func (b *Body) writeParts(mw *multipart.Writer) error {
	for _, name := range sortedKeys(b.Multipart) {
		p := b.Multipart[name]
		var w io.Writer
		var err error
		if p.Filename == "" {
			w, err = mw.CreateFormField(name)
		} else {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, p.Filename))
			h.Set("Content-Type", orDefault(p.ContentType, "application/octet-stream"))
			w, err = mw.CreatePart(h)
		}
		if err != nil {
			return err
		}
		if _, err := io.WriteString(w, p.Value); err != nil {
			return err
		}
	}
	return mw.Close()
}
