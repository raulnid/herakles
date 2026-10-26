package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/raulnid/herakles/internal/config"
	"github.com/raulnid/herakles/internal/engine"
	"github.com/raulnid/herakles/internal/logfmt"
	"github.com/raulnid/herakles/internal/metrics"
	_ "github.com/raulnid/herakles/internal/modules/httpstep"
)

// loadExample loads examples/herakles.yaml pointed at a test server.
func loadExample(t *testing.T, url string) *config.Config {
	t.Helper()
	cfg, err := config.Load("../herakles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Vars["api_key"] = config.Var{File: "../demo-api-key"}
	for i := range cfg.Targets {
		cfg.Targets[i].Vars["base_url"] = config.Var{Value: url}
	}
	if err := engine.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func runOnce(t *testing.T, cfg *config.Config) (bool, string, *prometheus.Registry) {
	t.Helper()
	var buf bytes.Buffer
	reg := prometheus.NewRegistry()
	obs := engine.Observers{logfmt.NewObserver(slog.New(logfmt.NewHandler(&buf, slog.LevelInfo))), metrics.New(reg)}
	ok := engine.New(cfg, obs).Once(context.Background())
	return ok, buf.String(), reg
}

func value(t *testing.T, reg *prometheus.Registry, metric string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != metric {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if want, ok := labels[lp.GetName()]; ok && want != lp.GetValue() {
					continue next
				}
			}
			return m.GetGauge().GetValue()
		}
	}
	t.Fatalf("no %s%v", metric, labels)
	return 0
}

func TestExampleJourney(t *testing.T) {
	mock := newServer("demo-api-key")
	srv := httptest.NewServer(mock)
	defer srv.Close()

	ok, log, reg := runOnce(t, loadExample(t, srv.URL))
	if !ok {
		t.Fatalf("cycle failed:\n%s", log)
	}
	for _, step := range []string{"token", "whoami", "no-token-rejected", "list", "upload", "listed", "download", "rename", "delete", "gone", "cleanup", "logout", "token-revoked"} {
		labor := map[string]string{"token": "login", "whoami": "login", "no-token-rejected": "login", "logout": "logout", "token-revoked": "logout"}[step]
		if labor == "" {
			labor = "projects"
		}
		if v := value(t, reg, "herakles_step_success", map[string]string{"target": "dev", "labor": labor, "step": step}); v != 1 {
			t.Errorf("dev %s/%s success = %v\n%s", labor, step, v, log)
		}
	}
	for _, step := range []string{"upload", "listed", "download", "rename", "delete", "gone", "cleanup"} {
		if v := value(t, reg, "herakles_step_skipped", map[string]string{"target": "prod", "step": step}); v != 1 {
			t.Errorf("prod %s must be skipped on a safe target", step)
		}
	}
	if v := value(t, reg, "herakles_transfer_bytes_per_second", map[string]string{"target": "dev", "step": "upload", "direction": "upload"}); v <= 0 {
		t.Errorf("upload rate = %v", v)
	}
	if n := testutil.CollectAndCount(reg, "herakles_http_phase_seconds"); n == 0 {
		t.Error("no http phase series")
	}
	if len(mock.projects) != 0 || len(mock.sessions) != 0 {
		t.Errorf("the journey left %d projects and %d sessions behind", len(mock.projects), len(mock.sessions))
	}
}

func TestExampleCorruptDownload(t *testing.T) {
	mock := newServer("demo-api-key")
	mock.corrupt.Store(true)
	srv := httptest.NewServer(mock)
	defer srv.Close()

	ok, log, reg := runOnce(t, loadExample(t, srv.URL))
	if ok {
		t.Fatal("a corrupted download must fail the cycle")
	}
	if v := value(t, reg, "herakles_labor_failed_step", map[string]string{"target": "dev", "labor": "projects", "step": "download"}); v != 1 {
		t.Errorf("failed_step{download} = %v", v)
	}
	if !strings.Contains(log, "!! FAILED") || !strings.Contains(log, "body sha256") {
		t.Errorf("log does not explain the failure:\n%s", log)
	}
	if v := value(t, reg, "herakles_step_success", map[string]string{"target": "dev", "labor": "logout", "step": "logout"}); v != 1 {
		t.Error("logout must run after a failed labor")
	}
	if len(mock.projects) != 0 {
		t.Errorf("finally did not clean up: %d projects left", len(mock.projects))
	}
}

func TestExampleBadAPIKey(t *testing.T) {
	srv := httptest.NewServer(newServer("another-key"))
	defer srv.Close()
	ok, log, _ := runOnce(t, loadExample(t, srv.URL))
	if ok {
		t.Fatal("a rejected API key must fail the cycle")
	}
	if !strings.Contains(log, `== skipped: labor "login" failed`) || strings.Contains(log, "demo-api-key") {
		t.Errorf("unexpected log:\n%s", log)
	}
}

// TestKubernetesConfig keeps the config shipped in the kustomization valid.
func TestKubernetesConfig(t *testing.T) {
	src, err := os.ReadFile("../kubernetes/herakles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}
