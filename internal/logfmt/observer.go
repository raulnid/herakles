package logfmt

import (
	"cmp"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/raulnid/herakles/internal/engine"
)

// Markers start every event message.
const (
	MarkStart    = ">>"
	MarkOK       = "ok"
	MarkOptional = "~~" // optional step failed, the labor goes on
	MarkFailed   = "!!" // required step failed, the labor stops
	MarkSkipped  = "--"
	MarkEnd      = "==" // labor or cycle summary
)

// Observer logs engine events. It never sees variable values: reports carry
// names and already redacted reasons only.
type Observer struct{ log *slog.Logger }

var _ engine.Observer = Observer{}

func NewObserver(l *slog.Logger) Observer { return Observer{log: l} }

func (o Observer) step(r engine.StepReport) *slog.Logger {
	pos := "fin"
	if !r.Finally {
		pos = fmt.Sprintf("%d/%d", r.N, r.Total)
	}
	return o.log.With(KeyTarget, r.Target, KeyLabor, r.Labor, KeyPos, pos, KeyStep, r.Step)
}

func (o Observer) StepStart(r engine.StepReport) {
	msg := MarkStart + " " + cmp.Or(r.Description, "start")
	if len(r.Needs) > 0 {
		msg += "  needs: " + strings.Join(r.Needs, ", ")
	}
	o.step(r).Info(msg)
}

func (o Observer) StepEnd(r engine.StepReport) {
	l := o.step(r)
	switch {
	case r.Status == engine.Skipped:
		l.Info(MarkSkipped + " skipped: " + r.Reason)
	case r.Status == engine.OK:
		msg := MarkOK + " " + Dur(r.Duration)
		if len(r.Captured) > 0 {
			msg += " -> " + strings.Join(r.Captured, ", ")
		}
		for _, t := range r.Transfers {
			msg += fmt.Sprintf("  %s %s", map[string]string{"upload": "up", "download": "down"}[t.Direction], Rate(t.BytesPerSecond()))
		}
		l.Info(msg)
	case r.Optional:
		l.Warn(fmt.Sprintf("%s failed after %s (optional, labor goes on): %s%s", MarkOptional, Dur(r.Duration), r.Reason, trace(r)))
	default:
		l.Error(fmt.Sprintf("%s FAILED after %s: %s%s", MarkFailed, Dur(r.Duration), r.Reason, trace(r)))
	}
}

func (o Observer) LaborEnd(r engine.LaborReport) {
	l := o.log.With(KeyTarget, r.Target, KeyLabor, r.Labor)
	switch r.Status {
	case engine.OK:
		l.Info(fmt.Sprintf("%s ok in %s", MarkEnd, Dur(r.Duration)))
	case engine.Failed:
		l.Error(fmt.Sprintf("%s FAILED at %q after %s", MarkEnd, r.FailedStep, Dur(r.Duration)))
	default:
		l.Info(fmt.Sprintf("%s skipped: %s", MarkEnd, r.Reason))
	}
}

func (o Observer) CycleEnd(r engine.CycleReport) {
	l := o.log.With(KeyTarget, r.Target, KeyLabor, "(cycle)")
	switch {
	case r.Err != nil:
		l.Error(fmt.Sprintf("%s FAILED: %s", MarkEnd, strings.ReplaceAll(r.Err.Error(), "\n", "; ")))
	case r.OK:
		l.Info(fmt.Sprintf("%s ok in %s", MarkEnd, Dur(r.Duration)))
	default:
		l.Error(fmt.Sprintf("%s FAILED in %s", MarkEnd, Dur(r.Duration)))
	}
}

// trace points a failure to the backend's trace of the same request.
func trace(r engine.StepReport) string {
	if r.TraceID == "" {
		return ""
	}
	return "  trace=" + r.TraceID
}

// Dur formats durations for humans: <1ms, 120ms, 1.24s.
func Dur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}

// Rate formats bytes per second with SI units.
func Rate(bps float64) string {
	for _, u := range []string{"B/s", "kB/s", "MB/s"} {
		if bps < 1000 {
			return fmt.Sprintf("%.1f %s", bps, u)
		}
		bps /= 1000
	}
	return fmt.Sprintf("%.1f GB/s", bps)
}
