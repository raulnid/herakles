// Package logfmt writes one human-readable line per event, with a fixed-width
// left zone (time, level, target, labor, position, step) and a "|" before the
// message, so columns line up in kubectl logs and stay greppable in Loki.
package logfmt

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"
)

// Attribute keys that fill the left zone instead of the message.
const (
	KeyTarget = "target"
	KeyLabor  = "labor"
	KeyPos    = "pos"
	KeyStep   = "step"
)

var widths = map[string]int{KeyTarget: 8, KeyLabor: 14, KeyPos: 5, KeyStep: 22}

type Handler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Leveler
	attrs []slog.Attr
}

func NewHandler(w io.Writer, level slog.Leveler) *Handler {
	return &Handler{mu: &sync.Mutex{}, w: w, level: level}
}

func (h *Handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	zone := map[string]string{}
	var rest strings.Builder
	add := func(a slog.Attr) bool {
		if _, ok := widths[a.Key]; ok {
			zone[a.Key] = a.Value.String()
		} else if a.Key != "" {
			fmt.Fprintf(&rest, " %s=%s", a.Key, a.Value)
		}
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)

	line := fmt.Sprintf("%s %-5s %s %s %s %s | %s%s\n",
		r.Time.Format("15:04:05.000"), r.Level,
		fit(zone[KeyTarget], widths[KeyTarget]), fit(zone[KeyLabor], widths[KeyLabor]),
		fit(zone[KeyPos], widths[KeyPos]), fit(zone[KeyStep], widths[KeyStep]),
		r.Message, rest.String())
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line)
	return err
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &c
}

// WithGroup is a no-op: herakles logs are flat.
func (h *Handler) WithGroup(string) slog.Handler { return h }

// fit pads or truncates s to exactly n runes.
func fit(s string, n int) string {
	if c := utf8.RuneCountInString(s); c <= n {
		return s + strings.Repeat(" ", n-c)
	}
	return string([]rune(s)[:n-1]) + "…"
}
