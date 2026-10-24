package payload

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func generate(t *testing.T, kind string, size Size) ([]byte, *Stream) {
	t.Helper()
	st, err := (&Spec{Kind: kind, Size: size}).Open()
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(st)
	if err != nil {
		t.Fatal(err)
	}
	return b, st
}

func checkSum(t *testing.T, b []byte, st *Stream) {
	t.Helper()
	sum, n, complete := st.Sum()
	want := sha256.Sum256(b)
	if sum != hex.EncodeToString(want[:]) || n != int64(len(b)) || !complete {
		t.Errorf("Sum = %s %d, want %x %d", sum, n, want, len(b))
	}
}

func incompressible(t *testing.T, b []byte) {
	t.Helper()
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(b)
	_ = w.Close()
	if float64(gz.Len()) < 0.99*float64(len(b)) {
		t.Errorf("gzip shrank %d bytes to %d: content is compressible", len(b), gz.Len())
	}
}

func TestRandom(t *testing.T) {
	b, st := generate(t, "random", 1<<20)
	if len(b) != 1<<20 {
		t.Errorf("size = %d", len(b))
	}
	checkSum(t, b, st)
	incompressible(t, b)
	other, _ := generate(t, "random", 1<<20)
	if bytes.Equal(b, other) {
		t.Error("two payloads are identical")
	}
}

func TestZip(t *testing.T) {
	const size = 5 << 20
	b, st := generate(t, "zip", size)
	checkSum(t, b, st)
	if len(b) < size || len(b) > size+512 {
		t.Errorf("archive size %d, want about %d", len(b), size)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(f) // also verifies the CRC
	if err != nil || len(content) != size {
		t.Fatalf("entry: %d bytes, %v", len(content), err)
	}
	incompressible(t, b)
}

func TestPNG(t *testing.T) {
	for _, size := range []Size{1, 100, 300 << 10} {
		b, st := generate(t, "png", size)
		checkSum(t, b, st)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		px := int64(img.Bounds().Dx() * img.Bounds().Dy() * 3)
		if px < int64(size)-3 || px > int64(size)+3*1024 {
			t.Errorf("size %d: %d bytes of pixels", size, px)
		}
	}
}

func TestSizeAndSpec(t *testing.T) {
	cases := map[string]Size{"512": 512, "10KB": 10_000, "5MiB": 5 << 20, "1 GiB": 1 << 30, "7B": 7}
	for in, want := range cases {
		var s Size
		if err := yaml.Unmarshal([]byte(in), &s); err != nil || s != want {
			t.Errorf("%q = %d, %v; want %d", in, s, err, want)
		}
	}
	var s Size
	if err := yaml.Unmarshal([]byte("5 MegaBytes"), &s); err == nil {
		t.Error("invalid size accepted")
	}
	if err := (&Spec{Kind: "nope", Size: 1}).Validate(); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("unknown kind: %v", err)
	}
	if err := (&Spec{Kind: "zip"}).Validate(); err == nil {
		t.Error("zero size accepted")
	}
	if sp := (&Spec{Kind: "png"}); sp.ContentType() != "image/png" || sp.Filename() != "payload.png" {
		t.Errorf("png metadata: %s %s", sp.ContentType(), sp.Filename())
	}
}

func TestNoDiskWrites(t *testing.T) {
	entries, _ := os.ReadDir(".")
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, _ := os.ReadFile(e.Name())
		for _, call := range []string{"os.Create", "os.WriteFile", "os.OpenFile", "os.CreateTemp"} {
			if bytes.Contains(src, []byte(call)) {
				t.Errorf("%s calls %s: payloads must stay in memory", e.Name(), call)
			}
		}
	}
}
