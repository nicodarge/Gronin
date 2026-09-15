package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/bintest"
	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
	"github.com/nicodarge/Gronin/runtime/internal/guard"
	"github.com/nicodarge/Gronin/runtime/internal/guard/guardtest"
	"github.com/nicodarge/Gronin/runtime/internal/record"
)

// deployment is one `gronin serve` in these tests: its own state directory, its own
// record, and the shared backend named in its coordination.json. Sharing one state
// directory would let the file lock give the right answer with the backend disconnected,
// so a guard that never consulted the backend would still pass.
type servingHost struct {
	stateDir string
	process  *bintest.Process
}

// cluster is two hosts, one embedded etcd, and the file the runs append a line to. The
// line is the run's own effect: a gather step runs only once the guard has admitted the
// run (FR-102), so a line is evidence of a run rather than of a log line about one.
type cluster struct {
	server *guardtest.Server
	lines  string
	hosts  []*servingHost
}

// tickPlaybook runs on the given schedule, and its gather step sleeps before leaving its
// mark so that both hosts' ticks land inside the same run.
const tickPlaybook = `
name: drift-check
trigger:
  type: cron
  schedule: "%s"
gather:
  - run: sleep %s; printf 'ran\n' >> %s
    as: facts.json
agent:
  model: claude-sonnet-5
  prompt_file: prompt.md
  tools: [Read]
  output_schema:
    type: object
sinks:
  - discord:
      webhook: http://127.0.0.1:9/unreachable
`

// newCluster lays out both hosts and the backend, and starts nothing.
func newCluster(t *testing.T, schedule, gatherSleep string) *cluster {
	t.Helper()
	server := guardtest.StartServer(t)
	shared := t.TempDir()
	lines := filepath.Join(shared, "lines")

	c := &cluster{server: server, lines: lines}
	for _, name := range []string{"host-a", "host-b"} {
		stateDir := filepath.Join(shared, name)
		books := filepath.Join(stateDir, "playbooks")
		if err := os.MkdirAll(books, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(books, "book.yaml"),
			[]byte(fmt.Sprintf(tickPlaybook, schedule, gatherSleep, lines)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(books, "prompt.md"), []byte("report"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeCoordinationFile(t, stateDir, map[string]any{
			"etcd": map[string]any{
				"endpoints": []string{server.Endpoint()},
				"prefix":    "gronin/",
			},
		})
		c.hosts = append(c.hosts, &servingHost{stateDir: stateDir})
	}
	return c
}

// writeCoordinationFile puts a coordination.json in a state directory.
func writeCoordinationFile(t *testing.T, stateDir string, document map[string]any) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, guard.CoordinationFile), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

// armWithin is how long a host is given to arm its schedules once started.
const armWithin = 60 * time.Second

// start starts one host and returns at once, before its schedules are armed.
func (c *cluster) start(t *testing.T, at int) *bintest.Process {
	t.Helper()
	host := c.hosts[at]
	host.process = bintest.Start(t, "serve",
		"--state-dir", host.stateDir, "--agent", fakeagent.Build(t),
		"--api-address", "127.0.0.1:0")
	return host.process
}

// ran is how many runs have left their mark.
func (c *cluster) ran(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(c.lines)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "ran\n")
}

// waitForRuns polls until want runs have left their mark, and fails if more do.
func (c *cluster) waitForRuns(t *testing.T, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		switch ran := c.ran(t); {
		case ran == want:
			return
		case ran > want:
			t.Fatalf("%d runs left their mark, want %d", ran, want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d run(s) in %s, want %d", c.ran(t), within, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// refusals is what `gronin refusals` prints for one host, read through the built
// executable the way an operator would.
func (c *cluster) refusals(t *testing.T, at int) string {
	t.Helper()
	got := bintest.Run(t, "refusals", "--state-dir", c.hosts[at].stateDir)
	if got.ExitCode != 0 {
		t.Fatalf("gronin refusals exited %d: %q", got.ExitCode, got.Stderr)
	}
	return got.Stdout
}

// SC-101, FR-101: two processes sharing one backend, one tick, exactly one run. The
// effect counted is the line the run appends, never a line claiming a claim was held.
//
// Both hosts are started concurrently and armed against one shared deadline, and the
// schedule is pinned to a single instant computed from it — the same fix #56 gave
// TestADelayedHostDoesNotRunATickAgain. Starting them one after another (c.serve, then
// c.serve again) let a loaded runner arm B after the wildcard schedule's tick had
// already been taken by A; B's next occurrence was then a minute away and nothing ever
// collided, which is exactly "neither host recorded a claim_held refusal" from CI run
// 34925617933.
//
// Pinning the instant alone is not enough: the loser's request also has to land while
// the winner's claim is still held, or it is refused as tick_already_ran instead. The
// gather step's sleep is what holds the claim open for that.
func TestTwoServesRunOneTickOnce(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	// Built before the deadline is taken, so that compiling is not charged to arming.
	bintest.Build(t)
	fakeagent.Build(t)

	armedBy := time.Now().UTC().Add(armWithin)
	// Both hosts must be armed, with margin, before the tick they will race for falls
	// due (#56's minute computation, including its boundary guard).
	at := armedBy.Add(10 * time.Second).Truncate(time.Minute).Add(time.Minute)
	if at.Minute() == 59 {
		at = at.Add(time.Minute)
	}
	// raceHold is how long the winning host holds its claim once it takes the tick,
	// which is the window the loser's own request has to land in to see the claim held
	// rather than already released. guard.DefaultConfig's DecisionBound (5s, and this
	// deployment's coordination.json declares none, so the default applies) bounds how
	// long a single Acquire decision is ever allowed to take; twice that, plus the same
	// 10s margin already given to arming above, comfortably exceeds the scheduling skew
	// two independently-woken processes can pick up reaching the same due instant under
	// load, without depending on how long a run or a poll takes relative to a cron period.
	const raceHold = 2*5*time.Second + 10*time.Second
	c := newCluster(t, fmt.Sprintf("%d %d %d %d *", at.Minute(), at.Hour(), at.Day(), int(at.Month())),
		fmt.Sprintf("%d", int(raceHold.Seconds())))

	host0 := c.start(t, 0)
	host1 := c.start(t, 1)
	host0.Expect(t, "armed 1 schedule", time.Until(armedBy))
	host1.Expect(t, "armed 1 schedule", time.Until(armedBy))
	if now := time.Now().UTC(); !now.Before(at) {
		t.Fatalf("both hosts were armed at %s, after the tick %s fell due", now, at)
	}

	// The tick falls due at `at`, and the run that takes it holds its claim for
	// raceHold, so waitForRuns has to cover both.
	c.waitForRuns(t, 1, time.Until(at)+raceHold+30*time.Second)
	// And stays one: the host that was refused must not run it a moment later.
	time.Sleep(5 * time.Second)
	if ran := c.ran(t); ran != 1 {
		t.Fatalf("%d runs of one tick", ran)
	}

	refused := c.refusals(t, 0) + c.refusals(t, 1)
	if !strings.Contains(refused, string(record.MechanismClaimHeld)) {
		t.Fatalf("neither host recorded a claim_held refusal:\n%s", refused)
	}
	// Naming the run that holds the claim is what makes the refusal actionable: an
	// identifier an operator can look up with `gronin show`.
	if !strings.Contains(refused, "run 20") {
		t.Fatalf("the refusal does not name the run holding the claim:\n%s", refused)
	}
}

// runs is what `gronin runs` reports at one host, status column included.
func (c *cluster) runs(t *testing.T, at int) string {
	t.Helper()
	got := bintest.Run(t, "runs", "--state-dir", c.hosts[at].stateDir)
	if got.ExitCode != 0 {
		t.Fatalf("gronin runs exited %d: %q", got.ExitCode, got.Stderr)
	}
	return got.Stdout
}
