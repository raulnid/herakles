package httpstep

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// tracer records httptrace events. Dial callbacks run on other goroutines,
// hence the mutex.
type tracer struct {
	mu                              sync.Mutex
	start, dnsStart, dnsDone        time.Time
	connStart, connDone             time.Time
	tlsStart, tlsDone, wrote, first time.Time
}

func (t *tracer) set(f *time.Time) {
	t.mu.Lock()
	if f.IsZero() {
		*f = time.Now()
	}
	t.mu.Unlock()
}

func (t *tracer) begin() { t.set(&t.start) }

func (t *tracer) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { t.set(&t.dnsStart) },
		DNSDone:      func(httptrace.DNSDoneInfo) { t.set(&t.dnsDone) },
		ConnectStart: func(string, string) { t.set(&t.connStart) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				t.set(&t.connDone)
			}
		},
		TLSHandshakeStart:    func() { t.set(&t.tlsStart) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { t.set(&t.tlsDone) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { t.set(&t.wrote) },
		GotFirstResponseByte: func() { t.set(&t.first) },
	}
}

// phases returns dns, connect, tls, wait (request written to first byte),
// transfer (first byte to end of body) and total. A reused connection has
// zero dns, connect and tls.
func (t *tracer) phases(end time.Time) map[string]time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	span := func(a, b time.Time) time.Duration {
		if a.IsZero() || b.IsZero() || b.Before(a) {
			return 0
		}
		return b.Sub(a)
	}
	return map[string]time.Duration{
		"dns":      span(t.dnsStart, t.dnsDone),
		"connect":  span(t.connStart, t.connDone),
		"tls":      span(t.tlsStart, t.tlsDone),
		"wait":     span(t.wrote, t.first),
		"transfer": span(t.first, end),
		"total":    span(t.start, end),
	}
}

// uploadTime is the time from the first body read to the request being written.
func (t *tracer) uploadTime(firstRead time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if firstRead.IsZero() || t.wrote.Before(firstRead) {
		return 0
	}
	return t.wrote.Sub(firstRead)
}
