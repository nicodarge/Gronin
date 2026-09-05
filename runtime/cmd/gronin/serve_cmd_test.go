package main

import (
	"errors"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/record"
	"github.com/nicodarge/Gronin/runtime/internal/run"
)

// The distinction this branch got wrong. The scheduler records a fire's error as a missed
// occurrence, so anything that returns an error here is claiming the occurrence never
// happened.
func TestOnlyAnOccurrenceThatNeverRanIsReportedMissed(t *testing.T) {
	for name, probe := range map[string]struct {
		finished record.Run
		err      error
		missed   bool
	}{
		"a run that succeeded":   {record.Run{ID: "r1", Status: record.StatusSucceeded}, nil, false},
		"a run that failed":      {record.Run{ID: "r1", Status: record.StatusFailed}, nil, false},
		"a run that was refused": {record.Run{ID: "r1", Status: record.StatusRefused}, nil, false},
		"a run that timed out":   {record.Run{ID: "r1", Status: record.StatusTimedOut}, nil, false},
		"a run that was capped":  {record.Run{ID: "r1", Status: record.StatusCapped}, nil, false},
		"the playbook was already in flight": {
			record.Run{}, run.ErrAlreadyRunning, true,
		},
		"the run could not begin":    {record.Run{}, errors.New("creating the working directory"), true},
		"neither a run nor an error": {record.Run{}, nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			got := scheduledOutcome(probe.finished, probe.err)
			if probe.missed && got == nil {
				t.Fatalf("%s was not reported as a missed occurrence", name)
			}
			if !probe.missed && got != nil {
				t.Fatalf("%s was reported as a missed occurrence: %v", name, got)
			}
		})
	}
}
