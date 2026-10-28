// Package junit records one cycle as a JUnit XML report, so CI systems such
// as Jenkins or GitLab show every step as a test: herakles run -once -report.
package junit

import (
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/raulnid/herakles/internal/engine"
)

// Report is an engine.Observer that collects every result. Write renders it.
type Report struct {
	mu     sync.Mutex
	suites map[string]*suite // by "target/labor"
}

var _ engine.Observer = (*Report)(nil)

type suite struct {
	Name     string     `xml:"name,attr"`
	Tests    int        `xml:"tests,attr"`
	Failures int        `xml:"failures,attr"`
	Skipped  int        `xml:"skipped,attr"`
	Time     string     `xml:"time,attr"`
	Cases    []testcase `xml:"testcase"`
	target   string
	order    int
}

type testcase struct {
	Name      string   `xml:"name,attr"`
	Classname string   `xml:"classname,attr"`
	Time      string   `xml:"time,attr"`
	Failure   *message `xml:"failure"`
	Skipped   *message `xml:"skipped"`
}

type message struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr,omitempty"`
	Text    string `xml:",chardata"`
}

func (r *Report) get(target, labor string) *suite {
	if r.suites == nil {
		r.suites = map[string]*suite{}
	}
	key := target + "/" + labor
	s, ok := r.suites[key]
	if !ok {
		s = &suite{Name: key, target: target, order: len(r.suites)}
		r.suites[key] = s
	}
	return s
}

func (r *Report) add(target, labor string, c testcase) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.get(target, labor)
	c.Classname = target + "." + labor
	s.Cases = append(s.Cases, c)
	s.Tests++
	if c.Failure != nil {
		s.Failures++
	}
	if c.Skipped != nil {
		s.Skipped++
	}
}

func (r *Report) StepStart(engine.StepReport) {}

func (r *Report) StepEnd(s engine.StepReport) {
	c := testcase{Name: s.Step, Time: seconds(s.Duration.Seconds())}
	if s.Finally {
		c.Name = "finally: " + s.Step
	}
	switch s.Status {
	case engine.Skipped:
		c.Skipped = &message{Message: s.Reason}
	case engine.Failed:
		kind := "required"
		if s.Optional {
			kind = "optional"
		}
		c.Failure = &message{Message: s.Reason, Type: kind}
		if s.TraceID != "" {
			c.Failure.Text = "trace=" + s.TraceID
		}
	}
	r.add(s.Target, s.Labor, c)
}

// LaborEnd records the steps of a skipped labor, which never reported any,
// and the labor's duration.
func (r *Report) LaborEnd(l engine.LaborReport) {
	if l.Status == engine.Skipped && l.Reason != "shutting down" {
		for _, step := range l.Steps {
			r.add(l.Target, l.Labor, testcase{Name: step, Time: seconds(0), Skipped: &message{Message: l.Reason}})
		}
	}
	r.mu.Lock()
	r.get(l.Target, l.Labor).Time = seconds(l.Duration.Seconds())
	r.mu.Unlock()
}

func (r *Report) CycleEnd(c engine.CycleReport) {
	if c.Err != nil {
		r.add(c.Target, "(cycle)", testcase{Name: "variables", Time: seconds(0), Failure: &message{Message: c.Err.Error(), Type: "required"}})
	}
}

// Write renders the report. Suites are grouped by target, in name order,
// and keep the labor order within a target.
func (r *Report) Write(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := make([]*suite, 0, len(r.suites))
	for _, s := range r.suites {
		if s.Time == "" {
			s.Time = seconds(0)
		}
		all = append(all, s)
	}
	slices.SortFunc(all, func(a, b *suite) int {
		if c := strings.Compare(a.target, b.target); c != 0 {
			return c
		}
		return a.order - b.order
	})
	doc := struct {
		XMLName  xml.Name `xml:"testsuites"`
		Name     string   `xml:"name,attr"`
		Tests    int      `xml:"tests,attr"`
		Failures int      `xml:"failures,attr"`
		Skipped  int      `xml:"skipped,attr"`
		Suites   []*suite `xml:"testsuite"`
	}{Name: "herakles", Suites: all}
	for _, s := range all {
		doc.Tests, doc.Failures, doc.Skipped = doc.Tests+s.Tests, doc.Failures+s.Failures, doc.Skipped+s.Skipped
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func seconds(s float64) string { return fmt.Sprintf("%.3f", s) }
