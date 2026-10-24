package engine

import (
	"time"

	"github.com/raulnid/herakles/internal/module"
)

type Status int

const (
	OK Status = iota
	Failed
	Skipped
)

func (s Status) String() string { return [...]string{"ok", "failed", "skipped"}[s] }

// StepReport describes one step. StepStart receives only the identity fields.
type StepReport struct {
	Target, Labor, Step, Description string
	N, Total                         int  // 1-based position in steps; 0 for finally steps
	Finally                          bool // part of the labor's finally block
	Optional                         bool
	Status                           Status
	Reason                           string // redacted error or skip reason
	Duration                         time.Duration
	Captured                         []string // names only, never values
	Phases                           map[string]time.Duration
	Transfers                        []module.Transfer
}

type LaborReport struct {
	Target, Labor string
	Status        Status
	FailedStep    string // the required step that failed the labor
	Reason        string
	Duration      time.Duration
	Steps         []string // every step name, steps then finally
}

type CycleReport struct {
	Target   string
	OK       bool
	Err      error // variable resolution error, if any
	Duration time.Duration
}

// Observer receives engine events. Implementations must be safe for
// concurrent use: targets run in parallel.
type Observer interface {
	StepStart(StepReport)
	StepEnd(StepReport)
	LaborEnd(LaborReport)
	CycleEnd(CycleReport)
}

// Observers fans events out to several observers.
type Observers []Observer

func (os Observers) StepStart(r StepReport) {
	for _, o := range os {
		o.StepStart(r)
	}
}

func (os Observers) StepEnd(r StepReport) {
	for _, o := range os {
		o.StepEnd(r)
	}
}

func (os Observers) LaborEnd(r LaborReport) {
	for _, o := range os {
		o.LaborEnd(r)
	}
}

func (os Observers) CycleEnd(r CycleReport) {
	for _, o := range os {
		o.CycleEnd(r)
	}
}
