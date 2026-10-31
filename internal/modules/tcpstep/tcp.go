// Package tcpstep is the tcp module: open a TCP connection, optionally
// complete a TLS handshake, and close it. It is the smallest real module, a
// reference for writing new ones.
package tcpstep

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/raulnid/herakles/internal/module"
)

func init() { module.Register("tcp", Module{}) }

type Config struct {
	Addr       string `yaml:"addr"` // host:port
	TLS        bool   `yaml:"tls"`
	ServerName string `yaml:"server_name"` // SNI and the name verified; default: the host of addr
}

type Module struct{}

func (Module) NewConfig() any { return &Config{} }

func (Module) Validate(cfg any) error {
	c := cfg.(*Config)
	var errs []error
	if c.Addr == "" {
		errs = append(errs, errors.New("addr is required"))
	} else if !module.Templated(c.Addr) {
		if _, _, err := net.SplitHostPort(c.Addr); err != nil {
			errs = append(errs, fmt.Errorf("addr: %w", err))
		}
	}
	if c.ServerName != "" && !c.TLS {
		errs = append(errs, errors.New("server_name needs tls: true"))
	}
	return errors.Join(errs...)
}

// Run times each phase on its own: resolve, then connect to the addresses in
// order, then the handshake with the target's TLS settings.
func (Module) Run(ctx context.Context, cfg any, env *module.Env) module.Result {
	c := cfg.(*Config)
	res := module.Result{Phases: map[string]time.Duration{}}
	start := time.Now()
	host, port, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return fail(&res, start, err)
	}

	t := time.Now()
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	res.Phases["dns"] = time.Since(t)
	if err != nil {
		return fail(&res, start, err)
	}

	t = time.Now()
	var d net.Dialer
	var conn net.Conn
	for _, ip := range ips {
		if conn, err = d.DialContext(ctx, "tcp", net.JoinHostPort(ip, port)); err == nil {
			break
		}
	}
	res.Phases["connect"] = time.Since(t)
	if err != nil {
		return fail(&res, start, err)
	}
	defer func() { _ = conn.Close() }()

	if c.TLS {
		tc := &tls.Config{}
		if env.TLSConfig != nil {
			tc = env.TLSConfig.Clone() // shared by the cycle: never modify it
		}
		tc.ServerName = cmp.Or(c.ServerName, host)
		t = time.Now()
		conn := tls.Client(conn, tc)
		err := conn.HandshakeContext(ctx)
		res.Phases["tls"] = time.Since(t)
		if err != nil {
			return fail(&res, start, err)
		}
		st := conn.ConnectionState()
		res.CertExpiry = module.CertExpiry(&st)
	}
	res.Phases["total"] = time.Since(start)
	return res
}

func fail(res *module.Result, start time.Time, err error) module.Result {
	res.Err = err
	res.Phases["total"] = time.Since(start)
	return *res
}
