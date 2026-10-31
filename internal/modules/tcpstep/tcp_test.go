package tcpstep

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/raulnid/herakles/internal/module"
	"github.com/raulnid/herakles/internal/testpki"
)

// listen accepts connections and, with cfg, completes the handshake.
func listen(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if cfg != nil {
				_ = tls.Server(conn, cfg).Handshake()
			}
			conn.Close()
		}
	}()
	return ln.Addr().String()
}

func run(ctx context.Context, c *Config, tc *tls.Config) module.Result {
	return Module{}.Run(ctx, c, &module.Env{TLSConfig: tc})
}

func TestConnect(t *testing.T) {
	res := run(context.Background(), &Config{Addr: listen(t, nil)}, nil)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	for _, p := range []string{"dns", "connect", "total"} {
		if _, ok := res.Phases[p]; !ok {
			t.Errorf("phase %s missing: %v", p, res.Phases)
		}
	}
	if _, ok := res.Phases["tls"]; ok || !res.CertExpiry.IsZero() {
		t.Errorf("plain TCP must not report TLS: %v %v", res.Phases, res.CertExpiry)
	}
}

func TestConnectionRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	res := run(context.Background(), &Config{Addr: addr}, nil)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "refused") {
		t.Errorf("err = %v, want connection refused", res.Err)
	}
	if res.Phases["total"] <= 0 {
		t.Errorf("failed step must still report phases: %v", res.Phases)
	}
}

func TestTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	res := run(ctx, &Config{Addr: listen(t, nil)}, nil)
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", res.Err)
	}
}

func TestTLS(t *testing.T) {
	ca := testpki.NewCA(t)
	srv := ca.Issue(t, 2, time.Now().Add(2*time.Hour))
	// mTLS: the server also checks the client cert from the target's settings.
	addr := listen(t, &tls.Config{Certificates: []tls.Certificate{srv.TLS}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca.Pool})
	client := ca.Issue(t, 3, time.Now().Add(time.Hour)).TLS
	target := &tls.Config{RootCAs: ca.Pool, Certificates: []tls.Certificate{client}}

	res := run(context.Background(), &Config{Addr: addr, TLS: true}, target)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Phases["tls"] <= 0 || !res.CertExpiry.Equal(srv.TLS.Leaf.NotAfter) {
		t.Errorf("phases %v, CertExpiry %v, want %v", res.Phases, res.CertExpiry, srv.TLS.Leaf.NotAfter)
	}
	if target.ServerName != "" {
		t.Error("the shared target config was modified")
	}

	res = run(context.Background(), &Config{Addr: addr, TLS: true}, &tls.Config{Certificates: []tls.Certificate{client}})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "certificate") {
		t.Errorf("untrusted server cert: err = %v", res.Err)
	}
	res = run(context.Background(), &Config{Addr: addr, TLS: true, ServerName: "other.example"}, target)
	if res.Err == nil {
		t.Error("server_name not in the cert must fail")
	}
}

func TestValidate(t *testing.T) {
	for cfg, want := range map[Config]string{
		{}:                             "addr is required",
		{Addr: "localhost"}:            "missing port",
		{Addr: "h:1", ServerName: "h"}: "needs tls",
		{Addr: "{{ .vars.addr }}"}:     "",
		{Addr: "h:1", TLS: true, ServerName: "h"}: "",
	} {
		err := Module{}.Validate(&cfg)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%+v: err = %v, want %q", cfg, err, want)
		}
	}
}
