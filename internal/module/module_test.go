package module

import (
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestDecode(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(`{url: "http://h/a?b=c#d: e", timeout: 2s, n: 3}`), &doc); err != nil {
		t.Fatal(err)
	}
	var c struct {
		URL     string        `yaml:"url"`
		Timeout time.Duration `yaml:"timeout"`
		N       int           `yaml:"n"`
	}
	if err := Decode(doc.Content[0], &c); err != nil {
		t.Fatal(err)
	}
	if c.URL != "http://h/a?b=c#d: e" || c.Timeout != 2*time.Second || c.N != 3 {
		t.Errorf("decoded %+v", c)
	}

	var strict struct {
		URL string `yaml:"url"`
	}
	if err := Decode(doc.Content[0], &strict); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown fields must be rejected, err = %v", err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer delete(registry, "dup-test")
	Register("dup-test", nil)
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
	}()
	Register("dup-test", nil)
}

func TestTransferRate(t *testing.T) {
	if r := (Transfer{Bytes: 10 << 20, Duration: 2 * time.Second}).BytesPerSecond(); r != 5<<20 {
		t.Errorf("rate = %v", r)
	}
	if r := (Transfer{Bytes: 1}).BytesPerSecond(); r != 0 {
		t.Errorf("zero duration rate = %v", r)
	}
}
