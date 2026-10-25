package vars

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raulnid/herakles/internal/config"
)

func TestRedact(t *testing.T) {
	v := New()
	v.Set("key", Value{Raw: "sk-live-123", Secret: true})
	v.Set("key_prefix", Value{Raw: "sk-live", Secret: true})
	v.Set("pin", Value{Raw: "12", Secret: true}) // too short, not masked
	v.Set("user", Value{Raw: "alice"})

	got := v.Redact(`401 {"error":"bad key sk-live-123","user":"alice","pin":"12"}`)
	want := `401 {"error":"bad key ***","user":"alice","pin":"12"}`
	if got != want {
		t.Errorf("Redact = %s\nwant      %s", got, want)
	}
	enc := New()
	enc.Set("t", Value{Raw: `a b/c"d`, Secret: true})
	if got := enc.Redact(`Get "http://h/?t=a+b%2Fc%22d" and {"t":"a b/c\"d"} and /p/a%20b%2Fc%22d`); got != `Get "http://h/?t=***" and {"t":"***"} and /p/***` {
		t.Errorf("encoded forms not masked: %s", got)
	}
	if New().Redact("x") != "x" {
		t.Error("empty Vars must not change input")
	}
}

func TestSetAndData(t *testing.T) {
	v := New()
	v.Set("a", Value{Raw: 1})
	v.Set("a", Value{Raw: "two"})
	if got, _ := v.Get("a"); got.Raw != "two" {
		t.Errorf("last write must win, got %v", got.Raw)
	}
	if v.Data()["a"] != "two" {
		t.Errorf("Data = %v", v.Data())
	}
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "api-key")
	write := func(s string) {
		if err := os.WriteFile(keyFile, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("first-key\n")
	t.Setenv("HERAKLES_TEST_URL", "http://env")

	global := map[string]config.Var{
		"api_key":  {File: keyFile},
		"base_url": {Value: "http://global"},
		"port":     {Value: 8080},
	}
	target := map[string]config.Var{"base_url": {Env: "HERAKLES_TEST_URL"}}

	v, err := Resolve(global, target)
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := v.Get("api_key"); k.Raw != "first-key" || !k.Secret {
		t.Errorf("file var = %+v, want trimmed and secret", k)
	}
	if u, _ := v.Get("base_url"); u.Raw != "http://env" || u.Secret {
		t.Errorf("target must override global: %+v", u)
	}
	if p, _ := v.Get("port"); p.Raw != 8080 {
		t.Errorf("value type lost: %#v", p.Raw)
	}

	write("rotated-key") // secret rotation between cycles
	v2, err := Resolve(global, target)
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := v2.Get("api_key"); k.Raw != "rotated-key" {
		t.Errorf("file not re-read: %v", k.Raw)
	}
	if v.Redact("rotated-key") != "rotated-key" || v2.Redact("rotated-key") != "***" {
		t.Error("each cycle must redact only its own secrets")
	}
}

func TestResolveErrors(t *testing.T) {
	_, err := Resolve(map[string]config.Var{
		"a": {File: "/nonexistent/key"},
		"b": {Env: "HERAKLES_TEST_UNSET"},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{`var "a"`, `var "b"`, "HERAKLES_TEST_UNSET is not set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
