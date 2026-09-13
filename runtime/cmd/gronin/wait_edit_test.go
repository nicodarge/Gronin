package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// SC-110, FR-121, and SC-114's surface, FR-125, single-host. The playbook is edited while a
// trigger waits, and the edit is looked for in what the run gathered — two versions that
// differ in what they produce, not in a field nothing reads. The other cases change the file
// in a way that must stop the trigger: a rename, since the name is the claim's identity; a
// declaration the load gate refuses, since the re-read is a load like any other (Principle
// I); a file that is gone; and a second file taking the name.
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

	for name, change := range map[string]struct {
		change func(t *testing.T, h *gatedHost)
		// names is what the playbook_changed refusal has to say about why.
		names string
	}{
		"renamed": {
			change: func(t *testing.T, h *gatedHost) { h.write(t, "drift-renamed", "renamed") },
			names:  "drift-renamed",
		},
		"refused": {
			change: func(t *testing.T, h *gatedHost) {
				h.writeDocument(t, h.book, strings.Replace(
					h.document("drift-check", "refused", ""), "tools: [Read]", "tools: [Bash]", 1))
			},
			names: `"Bash" is an unrestricted shell`,
		},
		"deleted": {
			change: func(t *testing.T, h *gatedHost) {
				if err := os.Remove(h.book); err != nil {
					t.Fatal(err)
				}
			},
			names: "cannot be loaded now",
		},
		"duplicated": {
			change: func(t *testing.T, h *gatedHost) {
				h.writeDocument(t, filepath.Join(h.books, "copy.yaml"), h.document("drift-check", "copy", ""))
			},
			names: "copy.yaml",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newGatedHost(t)
			h.hold(t)
			first := h.run(t)
			h.waitForRuns(t, 1)
			second := h.run(t)
			second.Expect(t, "waiting up to", time.Minute)

			change.change(t, h)
			h.release(t)
			if code, err := first.Wait(2 * time.Minute); err != nil || code != 0 {
				t.Fatalf("the first run exited %d, err %v: %s", code, err, first.Stderr())
			}
			second.Expect(t, string(record.MechanismPlaybookChanged), 2*time.Minute)
			if code, err := second.Wait(time.Minute); err != nil || code == 0 {
				t.Fatalf("the invocation whose playbook changed exited %d, err %v", code, err)
			}

			refused := h.refusals(t)
			if !strings.Contains(refused, string(record.MechanismPlaybookChanged)) ||
				!strings.Contains(refused, change.names) {
				t.Fatalf("no playbook_changed refusal saying %q:\n%s", change.names, refused)
			}
			if ran := h.ran(t); ran != 1 {
				t.Fatalf("%d runs, want only the first: the waiting trigger ran its changed playbook", ran)
			}
		})
	}
}
