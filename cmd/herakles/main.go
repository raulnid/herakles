// Command herakles runs multi-step synthetic probes and exposes Prometheus metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/raulnid/herakles/internal/config"
	"github.com/raulnid/herakles/internal/engine"
	"github.com/raulnid/herakles/internal/logfmt"
	"github.com/raulnid/herakles/internal/metrics"
	_ "github.com/raulnid/herakles/internal/modules/httpstep"
)

const usage = `usage:
  herakles run      -config FILE [-once] [-target NAME] [-log-level LEVEL]
  herakles validate -config FILE
  herakles version`

// version is set at build time: -ldflags "-X main.version=0.2.0".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if args[0] == "version" {
		fmt.Fprintf(stderr, "herakles %s %s\n", version, runtime.Version())
		return 0
	}
	config.DefaultUserAgent = "herakles/" + version + " (synthetic probe)"
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "herakles.yaml", "path to the config file")
	var once *bool
	var level slog.Level
	var target *string
	switch args[0] {
	case "run":
		once = fs.Bool("once", false, "run a single cycle and exit 1 if any required step failed")
		target = fs.String("target", "", "run only this target")
		fs.TextVar(&level, "log-level", slog.LevelInfo, "debug, info, warn or error; warn logs only failures")
	case "validate":
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err == nil && target != nil && *target != "" {
		err = cfg.OnlyTarget(*target)
	}
	if err == nil {
		err = engine.Validate(cfg)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", *cfgPath, err)
		return 1
	}
	if args[0] == "validate" {
		fmt.Fprintf(stderr, "%s: ok\n", *cfgPath)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log := slog.New(logfmt.NewHandler(stderr, level))
	slog.SetDefault(log)
	reg := prometheus.NewRegistry()
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "herakles_build_info", Help: "Always 1; labels carry the herakles and Go versions.",
		ConstLabels: prometheus.Labels{"version": version, "goversion": runtime.Version()},
	})
	buildInfo.Set(1)
	reg.MustRegister(buildInfo, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	e := engine.New(cfg, engine.Observers{logfmt.NewObserver(log), metrics.New(reg)})

	if *once {
		if !e.Once(ctx) {
			return 1
		}
		return 0
	}
	return serve(ctx, stop, cfg, reg, func() { e.Run(ctx) })
}

// serve exposes /metrics and /health while runEngine works. /health only says
// the process is alive: probe results must not make Kubernetes restart it.
func serve(ctx context.Context, stop func(), cfg *config.Config, reg *prometheus.Registry, runEngine func()) int {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	srv := &http.Server{Addr: cfg.Listen, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}

	failed := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
			stop()
		}
	}()
	slog.Info(fmt.Sprintf("herakles started: %d targets, %d labors, every %s, metrics on %s", //nolint:gosec // operator config, not user input
		len(cfg.Targets), len(cfg.Labors), cfg.Interval, cfg.Listen))
	runEngine()

	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	select {
	case err := <-failed:
		slog.Error("metrics server: " + err.Error())
		return 1
	default:
		slog.Info("herakles stopped")
		return 0
	}
}
