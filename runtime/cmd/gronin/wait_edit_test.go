package main

import (
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-110, FR-121, and SC-114's surface, FR-125, single-host. The playbook is edited while a
// trigger waits, and the edit is looked for in what the run gathered — two versions that
// differ in what they produce, not in a field nothing reads. A rename is the other half: the
// name is the claim's identity, so a trigger does not follow one.
func TestAWaitingTriggerRunsTheEditedPlaybook(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)

	t.Run("edited", func(t *testing.T) {
		h := newGatedHost(t)
		h.hold(t)
		first := h.run(t)
		h.waitForRuns(t, 1)
		second := h.run(t)
		second.Expect(t, "waiting up to", time.Minute)

		h.write(t, "drift-check", "edited")
		// Long enough that the time waited is plainly not nothing.
		time.Sleep(1500 * time.Millisecond)
		h.release(t)
		if code, err := first.Wait(2 * time.Minute); err != nil || code != 0 {
			t.Fatalf("the first run exited %d, err %v: %s", code, err, first.Stderr())
		}
		finished := second.Expect(t, "(waited", 2*time.Minute)
		if code, err := second.Wait(time.Minute); err != nil || code != 0 {
			t.Fatalf("the waiting invocation exited %d, err %v: %s", code, err, second.Stderr())
		}
		runID := strings.Fields(finished)[0]

		store := h.store(t)
		inputs, err := store.GatheredInputs(t.Context(), runID)
		if err != nil || len(inputs) != 1 {
			t.Fatalf("gathered inputs = %+v, err = %v", inputs, err)
		}
		gathered, err := store.Blobs().Get(inputs[0].BlobRef)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(gathered), `"version":"edited"`) {
			t.Fatalf("the run that waited gathered %q, not the edited playbook's output", gathered)
		}

		shown := bintest.Run(t, "show", runID, "--state-dir", h.stateDir)
		var waited time.Duration
		for _, line := range strings.Split(shown.Stdout, "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "waited" {
				if waited, err = time.ParseDuration(fields[1]); err != nil {
					t.Fatalf("show says it waited %q: %v", fields[1], err)
				}
			}
		}
		if waited < time.Second {
			t.Fatalf("gronin show does not say the run waited, or says %s:\n%s", waited, shown.Stdout)
		}
	})

	t.Run("renamed", func(t *testing.T) {
		h := newGatedHost(t)
		h.hold(t)
		first := h.run(t)
		h.waitForRuns(t, 1)
		second := h.run(t)
		second.Expect(t, "waiting up to", time.Minute)

		h.write(t, "drift-renamed", "renamed")
		h.release(t)
		if code, err := first.Wait(2 * time.Minute); err != nil || code != 0 {
			t.Fatalf("the first run exited %d, err %v: %s", code, err, first.Stderr())
		}
		second.Expect(t, string(record.MechanismPlaybookChanged), 2*time.Minute)
		if code, err := second.Wait(time.Minute); err != nil || code == 0 {
			t.Fatalf("the invocation whose playbook was renamed exited %d, err %v", code, err)
		}

		refused := h.refusals(t)
		if !strings.Contains(refused, string(record.MechanismPlaybookChanged)) ||
			!strings.Contains(refused, "drift-renamed") {
			t.Fatalf("no playbook_changed refusal naming what the file now declares:\n%s", refused)
		}
		if ran := h.ran(t); ran != 1 {
			t.Fatalf("%d runs, want only the first: the waiting trigger followed the rename", ran)
		}
	})
}
