package httpstep

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/raulnid/herakles/internal/module"
	"github.com/raulnid/herakles/internal/vars"
)

func config(t *testing.T, src string) *Config {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	c := &Config{}
	if err := module.Decode(doc.Content[0], c); err != nil {
		t.Fatal(err)
	}
	return c
}

func run(t *testing.T, client *http.Client, c *Config) module.Result {
	t.Helper()
	if client == nil {
		client = &http.Client{}
	}
	return Module{}.Run(context.Background(), c, &module.Env{Target: "dev", Vars: vars.New(), HTTPClient: client, UserAgent: "herakles-test"})
}

func TestJSONRequestCaptureAndAssert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
			r.Header.Get("User-Agent") != "herakles-test" || r.Header.Get("X-Key") != "k" || in["api_key"] != "0123" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Session", "s-1")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"token":"tok","user":{"id":7,"roles":["a","b"]}}`)
	}))
	defer srv.Close()

	res := run(t, nil, config(t, `
method: post
url: `+srv.URL+`
headers: {X-Key: k}
body: {json: {api_key: "0123"}}
capture:
  token: {jsonpath: $.token, secret: true}
  session: {header: X-Session}
  code: {status: true}
  role: {jsonpath: "$.user.roles[1]"}
assert:
  jsonpath: {$.user.id: 7}
`))
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	want := map[string]vars.Value{
		"token": {Raw: "tok", Secret: true}, "session": {Raw: "s-1"}, "code": {Raw: 201}, "role": {Raw: "b"},
	}
	for k, v := range want {
		if res.Captured[k] != v {
			t.Errorf("captured %s = %#v, want %#v", k, res.Captured[k], v)
		}
	}
	if res.Phases["total"] <= 0 || res.Phases["connect"] <= 0 {
		t.Errorf("phases not measured: %v", res.Phases)
	}
	if len(res.Transfers) != 0 {
		t.Errorf("a small JSON request is not a transfer: %+v", res.Transfers)
	}
}

func TestStatusAssertions(t *testing.T) {
	long := strings.Repeat("x", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/denied":
			http.Error(w, "no token", http.StatusUnauthorized)
		case "/broken":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "line1\nline2 "+long)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()

	cases := []struct{ name, cfg, wantErr string }{
		{"default accepts 2xx", "url: " + srv.URL + "/ok", ""},
		{"expected rejection", "url: " + srv.URL + "/denied\nassert: {status: [401, 403]}", ""},
		{"rejection did not happen", "url: " + srv.URL + "/ok\nassert: {status: [401, 403]}", "status 200, want [401 403]"},
		{"server error", "url: " + srv.URL + "/broken", "status 500, want 2xx; body: line1 line2 xxx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := run(t, nil, config(t, tc.cfg)).Err
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), long[:excerptLen]) {
				t.Errorf("error carries more than %d bytes of body", excerptLen)
			}
		})
	}
}

func TestDownloadChecksum(t *testing.T) {
	payload := strings.Repeat("herakles", 64<<10) // 512 KiB
	sum := sha256.Sum256([]byte(payload))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	ok := config(t, "url: "+srv.URL+"\nassert: {sha256: "+strings.ToUpper(hex.EncodeToString(sum[:]))+", size: 524288}")
	if !(Module{}).IsTransfer(ok) {
		t.Error("a checksum assert makes the step a transfer")
	}
	res := run(t, nil, ok)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(res.Transfers) != 1 || res.Transfers[0].Direction != "download" || res.Transfers[0].Bytes != 524288 {
		t.Errorf("transfers = %+v", res.Transfers)
	}

	bad := run(t, nil, config(t, "url: "+srv.URL+"\nassert: {sha256: "+strings.Repeat("0", 64)+"}"))
	if bad.Err == nil || !strings.Contains(bad.Err.Error(), "body sha256 "+hex.EncodeToString(sum[:])) {
		t.Errorf("checksum mismatch: err = %v", bad.Err)
	}
	short := run(t, nil, config(t, "url: "+srv.URL+"\nassert: {size: 10}"))
	if short.Err == nil || !strings.Contains(short.Err.Error(), "body size 524288 bytes, want 10") {
		t.Errorf("size mismatch: err = %v", short.Err)
	}
}

func TestMultipart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f, h, err := r.FormFile("doc")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		content, _ := io.ReadAll(f)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"name": r.FormValue("name"), "file": h.Filename, "type": h.Header.Get("Content-Type"), "content": string(content),
		})
	}))
	defer srv.Close()

	res := run(t, nil, config(t, `
method: POST
url: `+srv.URL+`
body:
  multipart:
    name: synthetic-1
    doc: {value: "hello", filename: notes.txt, content_type: text/plain}
assert:
  jsonpath: {$.name: synthetic-1, $.file: notes.txt, $.type: text/plain, $.content: hello}
`))
	if res.Err != nil {
		t.Fatal(res.Err)
	}
}

func TestFailures(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	text := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"a":1}`)
	}))
	defer text.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res := Module{}.Run(ctx, config(t, "url: "+slow.URL), &module.Env{Vars: vars.New(), HTTPClient: &http.Client{}})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "deadline exceeded") {
		t.Errorf("timeout: err = %v", res.Err)
	}

	if err := run(t, nil, config(t, "url: "+down.URL)).Err; err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("endpoint down: err = %v", err)
	}
	if err := run(t, nil, config(t, "url: "+text.URL+"\ncapture: {x: {jsonpath: $.missing}}")).Err; err == nil || !strings.Contains(err.Error(), "matched nothing") {
		t.Errorf("missing jsonpath: err = %v", err)
	}
	if err := run(t, nil, config(t, "url: "+text.URL+"\nassert: {jsonpath: {$.a: 2}}")).Err; err == nil || !strings.Contains(err.Error(), "$.a = 1, want 2") {
		t.Errorf("jsonpath mismatch: err = %v", err)
	}
	if err := run(t, nil, config(t, "url: "+text.URL+"\ncapture: {x: {header: X-Nope}}")).Err; err == nil || !strings.Contains(err.Error(), "header X-Nope") {
		t.Errorf("missing header: err = %v", err)
	}
}

func TestNotJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html>gateway</html>")
	}))
	defer srv.Close()
	err := run(t, nil, config(t, "url: "+srv.URL+"\ncapture: {x: {jsonpath: $.a}}")).Err
	if err == nil || !strings.Contains(err.Error(), "not JSON") || !strings.Contains(err.Error(), "<html>gateway") {
		t.Errorf("err = %v", err)
	}
}

func TestTLSPhasesAndReuse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	client := srv.Client()

	first := run(t, client, config(t, "url: "+srv.URL))
	if first.Err != nil {
		t.Fatal(first.Err)
	}
	if first.Phases["tls"] <= 0 || first.Phases["wait"] <= 0 {
		t.Errorf("first request phases: %v", first.Phases)
	}
	second := run(t, client, config(t, "url: "+srv.URL))
	if second.Phases["tls"] != 0 || second.Phases["connect"] != 0 {
		t.Errorf("reused connection must report zero tls and connect: %v", second.Phases)
	}
}

func TestValidate(t *testing.T) {
	err := Module{}.Validate(config(t, `
body: {json: {}, text: x}
capture:
  both: {jsonpath: $.a, header: X}
  none: {secret: true}
  bad: {jsonpath: "$[?"}
assert: {status: [700], jsonpath: {"$..[": 1}, sha256: abc}
`))
	for _, want := range []string{"only one of json, text, multipart or payload", `assert sha256 "abc": want 64 hex`, `capture "both"`, `capture "none"`, `capture "bad": jsonpath`, "status 700", `assert jsonpath "$..["`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error %v does not mention %q", err, want)
		}
	}
	if err := (Module{}).Validate(config(t, "url: http://x\ncapture: {a: {status: true}}")); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

func TestOutputs(t *testing.T) {
	got := Module{}.Outputs(config(t, "capture: {a: {status: true}, b: {header: X}}"))
	if len(got) != 2 {
		t.Errorf("Outputs = %v", got)
	}
}

// store is a fake storage API: it hashes what it receives and serves it back.
func store(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	var saved []byte
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write(saved)
			return
		case strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/"):
			f, h, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			saved, _ = io.ReadAll(f)
			w.Header().Set("X-Filename", h.Filename)
			w.Header().Set("X-Type", h.Header.Get("Content-Type"))
		default:
			saved, _ = io.ReadAll(r.Body)
			w.Header().Set("X-Type", r.Header.Get("Content-Type"))
		}
		sum := sha256.Sum256(saved)
		_ = json.NewEncoder(w).Encode(map[string]any{"sha256": hex.EncodeToString(sum[:]), "size": len(saved)})
	}))
}

func TestPayloadUploadAndVerify(t *testing.T) {
	srv := store(t)
	defer srv.Close()

	for name, body := range map[string]string{
		"multipart": "{multipart: {meta: synthetic, file: {payload: {kind: zip, size: 1MiB, as: upload}}}}",
		"raw":       "{payload: {kind: png, size: 64KiB, as: upload}}",
	} {
		t.Run(name, func(t *testing.T) {
			c := config(t, "method: POST\nurl: "+srv.URL+"\nbody: "+body+"\ncapture: {server_sum: {jsonpath: $.sha256}, ctype: {header: X-Type}}")
			if !(Module{}).IsTransfer(c) || len(Module{}.Outputs(c)) != 3 {
				t.Errorf("IsTransfer/Outputs wrong for a payload body: %v", Module{}.Outputs(c))
			}
			res := run(t, nil, c)
			if res.Err != nil {
				t.Fatal(res.Err)
			}
			up, _ := res.Captured["upload"].Raw.(map[string]any)
			if up == nil || up["sha256"] != res.Captured["server_sum"].Raw {
				t.Fatalf("payload sha256 %v does not match what the server stored %v", up, res.Captured["server_sum"].Raw)
			}
			if want := map[string]string{"multipart": "application/zip", "raw": "image/png"}[name]; res.Captured["ctype"].Raw != want {
				t.Errorf("content type %v, want %s", res.Captured["ctype"].Raw, want)
			}
			if len(res.Transfers) != 1 || res.Transfers[0].Direction != "upload" || res.Transfers[0].Bytes < up["size"].(int64) {
				t.Errorf("transfers = %+v", res.Transfers)
			}

			// Download what was stored and verify it against the upload.
			down := run(t, nil, config(t, fmt.Sprintf("url: %s\nassert: {sha256: %s, size: %d}", srv.URL, up["sha256"], up["size"])))
			if down.Err != nil {
				t.Fatal(down.Err)
			}
		})
	}
}

func TestPayloadUploadNotReadByServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{}") // answers without reading the 8 MiB body
	}))
	defer srv.Close()
	res := run(t, nil, config(t, "method: PUT\nurl: "+srv.URL+"\nbody: {payload: {kind: random, size: 8MiB, as: up}}"))
	if res.Err == nil {
		t.Fatal("a payload the server did not receive must not be published as uploaded")
	}
}

func TestPayloadValidate(t *testing.T) {
	err := Module{}.Validate(config(t, "body: {multipart: {f: {value: x, payload: {kind: gif, size: 1}}}}"))
	for _, want := range []string{"value or payload, not both", `unknown payload kind "gif"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error %v does not mention %q", err, want)
		}
	}
}

func TestSHA256FromResponseHeader(t *testing.T) {
	const content = "stored bytes"
	sum := sha256.Sum256([]byte(content))
	hexSum, b64Sum := hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(sum[:])
	wrong := sha256.Sum256([]byte("other"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Hex", hexSum)
		w.Header().Set("X-Amz-Checksum-Sha256", b64Sum)
		w.Header().Set("Content-Digest", "sha-512=:AAAA:, sha-256=:"+b64Sum+":")
		w.Header().Set("Digest", "SHA-256="+b64Sum)
		w.Header().Set("X-Wrong", hex.EncodeToString(wrong[:]))
		w.Header().Set("X-Garbage", "md5=abc")
		_, _ = io.WriteString(w, content)
	}))
	defer srv.Close()

	for _, h := range []string{"X-Hex", "X-Amz-Checksum-Sha256", "Content-Digest", "Digest"} {
		c := config(t, "url: "+srv.URL+"\nassert: {sha256_header: "+h+"}")
		if !(Module{}).IsTransfer(c) {
			t.Errorf("%s: a header checksum makes the step a transfer", h)
		}
		if err := run(t, nil, c).Err; err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
	both := run(t, nil, config(t, "url: "+srv.URL+"\nassert: {sha256: "+hexSum+", sha256_header: Content-Digest}"))
	if both.Err != nil {
		t.Errorf("sha256 and sha256_header together: %v", both.Err)
	}
	for h, want := range map[string]string{
		"X-Wrong":   "but header X-Wrong says " + hex.EncodeToString(wrong[:]),
		"X-Garbage": "not a SHA-256 digest",
		"X-Missing": "header X-Missing not in response",
	} {
		err := run(t, nil, config(t, "url: "+srv.URL+"\nassert: {sha256_header: "+h+"}")).Err
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", h, err, want)
		}
	}
}

func TestTrailingDataIsNotJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"a":1} trailing`)
	}))
	defer srv.Close()
	err := run(t, nil, config(t, "url: "+srv.URL+"\ncapture: {a: {jsonpath: $.a}}")).Err
	if err == nil || !strings.Contains(err.Error(), "data after the first value") {
		t.Errorf("err = %v", err)
	}
}

func TestLargeIntegerCapturedExactly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":9007199254740993}`)
	}))
	defer srv.Close()
	res := run(t, nil, config(t, "url: "+srv.URL+"\ncapture: {id: {jsonpath: $.id}}"))
	if res.Err != nil || fmt.Sprint(res.Captured["id"].Raw) != "9007199254740993" {
		t.Errorf("id = %v, err %v", res.Captured["id"].Raw, res.Err)
	}
}

func TestRedirectToOtherHostDropsSecretHeaders(t *testing.T) {
	got := make(chan http.Header, 1)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Clone() }))
	defer other.Close()
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		if r.URL.Path == "/next" {
			if r.Header.Get("X-Api-Key") == "" {
				http.Error(w, "same-host redirect lost the key", http.StatusBadRequest)
			}
			return
		}
		http.Redirect(w, r, other.URL+"/file", http.StatusFound)
	}))
	defer same.Close()

	v := vars.New()
	v.Set("key", vars.Value{Raw: "secret-key-123", Secret: true})
	env := &module.Env{Vars: v, HTTPClient: &http.Client{}}
	headers := "headers: {X-Api-Key: secret-key-123, X-Trace: plain}"

	if res := (Module{}).Run(context.Background(), config(t, "url: "+same.URL+"/start\n"+headers), env); res.Err != nil {
		t.Errorf("same-host redirect: %v", res.Err)
	}
	if res := (Module{}).Run(context.Background(), config(t, "url: "+same.URL+"/away\n"+headers), env); res.Err != nil {
		t.Fatal(res.Err)
	}
	h := <-got
	if h.Get("X-Api-Key") != "" || h.Get("X-Trace") != "plain" {
		t.Errorf("headers sent to the other host: %v", h)
	}
}

func TestHeaderAndPartInjectionRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	err := run(t, nil, config(t, "url: "+srv.URL+"\nheaders: {X-Id: \"1\\r\\nX-Injected: yes\"}")).Err
	if err == nil || !strings.Contains(err.Error(), "invalid header field value") {
		t.Errorf("CRLF in a header value: err = %v", err)
	}
	err = run(t, nil, config(t, "method: POST\nurl: "+srv.URL+"\nbody: {multipart: {f: {value: x, filename: a.txt, content_type: \"text/plain\\r\\nX-Injected: yes\"}}}")).Err
	if err == nil || !strings.Contains(err.Error(), "line break") {
		t.Errorf("CRLF in a part content type: err = %v", err)
	}
}

func TestTraceParentHeader(t *testing.T) {
	got := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Get("Traceparent") }))
	defer srv.Close()
	const tp = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	env := &module.Env{Vars: vars.New(), HTTPClient: &http.Client{}, TraceParent: tp}
	if res := (Module{}).Run(context.Background(), config(t, "url: "+srv.URL), env); res.Err != nil {
		t.Fatal(res.Err)
	}
	if h := <-got; h != tp {
		t.Errorf("traceparent = %q", h)
	}
	own := "00-11111111111111111111111111111111-2222222222222222-00"
	if res := (Module{}).Run(context.Background(), config(t, "url: "+srv.URL+"\nheaders: {traceparent: "+own+"}"), env); res.Err != nil {
		t.Fatal(res.Err)
	}
	if h := <-got; h != own {
		t.Errorf("a traceparent set in headers must win, got %q", h)
	}
}
