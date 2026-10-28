package junit

import (
	"bytes"
	"encoding/xml"
	"errors"
	"testing"
	"time"

	"github.com/raulnid/herakles/internal/engine"
)

func TestReport(t *testing.T) {
	r := &Report{}
	step := func(target, labor, name string, st engine.Status, optional bool, reason string) {
		r.StepEnd(engine.StepReport{Target: target, Labor: labor, Step: name, Status: st, Optional: optional,
			Reason: reason, Duration: 1500 * time.Millisecond, TraceID: "abc"})
	}
	step("prod", "login", "token", engine.OK, false, "")
	step("dev", "login", "token", engine.OK, false, "")
	step("dev", "login", "extra", engine.Failed, true, "status 404 <&>")
	step("dev", "projects", "upload", engine.Failed, false, "status 500")
	r.StepEnd(engine.StepReport{Target: "dev", Labor: "projects", Step: "cleanup", Finally: true, Status: engine.Skipped, Reason: "needs id"})
	r.LaborEnd(engine.LaborReport{Target: "dev", Labor: "projects", Status: engine.Failed, Duration: 2 * time.Second})
	r.LaborEnd(engine.LaborReport{Target: "dev", Labor: "after", Status: engine.Skipped, Reason: `labor "projects" failed`, Steps: []string{"a", "b"}})
	r.CycleEnd(engine.CycleReport{Target: "staging", Err: errors.New(`var "key": no such file`)})

	var buf bytes.Buffer
	if err := r.Write(&buf); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tests    int `xml:"tests,attr"`
		Failures int `xml:"failures,attr"`
		Skipped  int `xml:"skipped,attr"`
		Suites   []struct {
			Name  string `xml:"name,attr"`
			Time  string `xml:"time,attr"`
			Cases []struct {
				Name    string `xml:"name,attr"`
				Class   string `xml:"classname,attr"`
				Failure *struct {
					Message string `xml:"message,attr"`
					Type    string `xml:"type,attr"`
					Text    string `xml:",chardata"`
				} `xml:"failure"`
				Skipped *struct {
					Message string `xml:"message,attr"`
				} `xml:"skipped"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, buf.String())
	}
	if doc.Tests != 8 || doc.Failures != 3 || doc.Skipped != 3 {
		t.Errorf("totals: %d tests, %d failures, %d skipped\n%s", doc.Tests, doc.Failures, doc.Skipped, buf.String())
	}
	var names []string
	for _, s := range doc.Suites {
		names = append(names, s.Name)
	}
	want := []string{"dev/login", "dev/projects", "dev/after", "prod/login", "staging/(cycle)"}
	if len(names) != len(want) {
		t.Fatalf("suites %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("suite %d = %s, want %s", i, names[i], want[i])
		}
	}
	extra := doc.Suites[0].Cases[1]
	if extra.Class != "dev.login" || extra.Failure == nil || extra.Failure.Type != "optional" || extra.Failure.Message != "status 404 <&>" || extra.Failure.Text != "trace=abc" {
		t.Errorf("optional failure: %+v", extra)
	}
	if up := doc.Suites[1].Cases[0]; up.Failure == nil || up.Failure.Type != "required" {
		t.Errorf("required failure: %+v", up)
	}
	if c := doc.Suites[1].Cases[1]; c.Name != "finally: cleanup" || c.Skipped == nil || doc.Suites[1].Time != "2.000" {
		t.Errorf("finally step or labor time: %+v, time %s", c, doc.Suites[1].Time)
	}
	if c := doc.Suites[2].Cases; len(c) != 2 || c[0].Skipped == nil || c[0].Skipped.Message != `labor "projects" failed` {
		t.Errorf("skipped labor: %+v", c)
	}
	if c := doc.Suites[4].Cases[0]; c.Name != "variables" || c.Failure == nil {
		t.Errorf("cycle error: %+v", c)
	}
}
