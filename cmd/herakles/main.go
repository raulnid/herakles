// Command herakles runs multi-step synthetic probes and exposes Prometheus metrics.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/raulnid/herakles/internal/config"
	"github.com/raulnid/herakles/internal/engine"
	_ "github.com/raulnid/herakles/internal/modules/httpstep"
)

const usage = `usage:
  herakles run      -config FILE [-once] [-target NAME]
  herakles validate -config FILE`

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "herakles.yaml", "path to the config file")
	var once *bool
	var target *string
	switch args[0] {
	case "run":
		once = fs.Bool("once", false, "run a single cycle and exit 1 if any required step failed")
		target = fs.String("target", "", "run only this target")
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
	e := engine.New(cfg, nil)
	if *once {
		if !e.Once(ctx) {
			return 1
		}
		return 0
	}
	e.Run(ctx)
	return 0
}
