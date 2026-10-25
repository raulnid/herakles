package logfmt

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raulnid/herakles/internal/config"
	"github.com/raulnid/herakles/internal/engine"
	"github.com/raulnid/herakles/internal/module"
	_ "github.com/raulnid/herakles/internal/modules/httpstep"
)

func newObserver(buf *bytes.Buffer) Observer {
	return NewObserver(slog.New(NewHandler(buf, slog.LevelInfo)))
}

// lines drops the timestamp, which is the only varying column.
func lines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		out = append(out, l[len("15:04:05.000 "):])
	}
	return out
}

func TestGoldenLines(t *testing.T) {
	var buf bytes.Buffer
	o := newObserver(&buf)
	up := engine.StepReport{Target: "dev", Labor: "projects", Step: "upload", N: 2, Total: 4, Description: "Upload a 5 MiB zip", Needs: []string{"base_url", "token"}}
	o.StepStart(up)
	up.Status, up.Duration, up.Captured = engine.OK, 787*time.Millisecond, []string{"project_id", "upload"}
	up.Transfers = []module.Transfer{{Direction: "upload", Bytes: 6_300_000, Duration: time.Second}}
	o.StepEnd(up)
	o.StepEnd(engine.StepReport{Target: "dev", Labor: "projects", Step: "thumbnail", N: 3, Total: 4, Optional: true, Status: engine.Failed, Duration: 35 * time.Millisecond, Reason: "status 404, want 2xx"})
	o.StepEnd(engine.StepReport{Target: "prod", Labor: "projects", Step: "upload", N: 2, Total: 4, Status: engine.Skipped, Reason: "mutating step on a safe target"})
	o.StepEnd(engine.StepReport{Target: "dev", Labor: "projects", Step: "download", N: 4, Total: 4, Status: engine.Failed, Duration: 1200 * time.Microsecond, Reason: "body sha256 ab, want cd"})
	o.StepEnd(engine.StepReport{Target: "dev", Labor: "projects", Step: "cleanup-a-very-long-step-name", Finally: true, Status: engine.OK, Duration: 400 * time.Microsecond})
	o.LaborEnd(engine.LaborReport{Target: "dev", Labor: "projects", Status: engine.Failed, FailedStep: "download", Duration: 1284 * time.Millisecond})
	o.LaborEnd(engine.LaborReport{Target: "dev", Labor: "logout", Status: engine.OK, Duration: 12 * time.Millisecond})
	o.CycleEnd(engine.CycleReport{Target: "staging", Err: errors.New("var \"key\": no such file\nvar \"b\": unset")})

	want := []string{
		`INFO  dev      projects       2/4   upload                 | >> Upload a 5 MiB zip  needs: base_url, token`,
		`INFO  dev      projects       2/4   upload                 | ok 787ms -> project_id, upload  up 6.3 MB/s`,
		`WARN  dev      projects       3/4   thumbnail              | ~~ failed after 35ms (optional, labor goes on): status 404, want 2xx`,
		`INFO  prod     projects       2/4   upload                 | -- skipped: mutating step on a safe target`,
		`ERROR dev      projects       4/4   download               | !! FAILED after 1ms: body sha256 ab, want cd`,
		`INFO  dev      projects       fin   cleanup-a-very-long-s… | ok <1ms`,
		`ERROR dev      projects                                    | == FAILED at "download" after 1.28s`,
		`INFO  dev      logout                                      | == ok in 12ms`,
		`ERROR staging  (cycle)                                     | == FAILED: var "key": no such file; var "b": unset`,
	}
	got := lines(&buf)
	if len(got) != len(want) {
		t.Fatalf("got %d lines:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestHandlerExtraAttrsAndLevel(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewHandler(&buf, slog.LevelWarn))
	l.Info("hidden")
	l.With("target", "dev").Warn("cycle took longer than interval", "took", time.Second)
	got := lines(&buf)
	if len(got) != 1 || !strings.HasSuffix(got[0], "| cycle took longer than interval took=1s") || !strings.HasPrefix(got[0], "WARN  dev ") {
		t.Errorf("got %q", got)
	}
}

// TestSecretsNeverLogged runs a real cycle whose server echoes credentials in
// its error bodies, and checks that no secret reaches the log.
func TestSecretsNeverLogged(t *testing.T) {
	const apiKey, token = "ak-file-secret-1234", "tok-captured-secret-5678"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/login":
			_, _ = io.WriteString(w, `{"token":"`+token+`"}`)
		case "/echo":
			http.Error(w, "denied for "+r.Header.Get("Authorization")+" body "+string(body), http.StatusForbidden)
		}
	}))
	defer srv.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(apiKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(`
vars: {key: {file: ` + keyFile + `}, base: {value: "` + srv.URL + `"}}
targets: [{name: dev}]
labors:
  - name: l
    steps:
      - name: login
        http: {method: POST, url: "{{ .vars.base }}/login", body: {json: {key: "{{ .vars.key }}"}}, capture: {token: {jsonpath: $.token, secret: true}}}
      - name: optional-echo
        optional: true
        http: {method: POST, url: "{{ .vars.base }}/echo", headers: {Authorization: "Bearer {{ .vars.token }}"}, body: {text: "{{ .vars.key }}"}}
      - name: echo
        http: {url: "{{ .vars.base }}/echo?k={{ .vars.key }}", headers: {Authorization: "{{ .vars.token }}"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	engine.New(cfg, newObserver(&buf)).Once(context.Background())
	out := buf.String()
	for _, secret := range []string{apiKey, token} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked into the log:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "denied for Bearer *** body ***") || !strings.Contains(out, "!! FAILED") {
		t.Errorf("expected redacted failures in the log:\n%s", out)
	}
}
