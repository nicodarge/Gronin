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

// tickPlaybook runs on the given schedule, and its gather step runs `before` before
// leaving its mark, so a test can control what has to happen before both hosts' ticks
// land inside the same run.
const tickPlaybook = `
name: drift-check
trigger:
  type: cron
  schedule: "%s"
gather:
  - run: %s; printf 'ran\n' >> %s
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
func newCluster(t *testing.T, schedule, before string) *cluster {
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
			[]byte(fmt.Sprintf(tickPlaybook, schedule, before, lines)), 0o600); err != nil {
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

// shellSingleQuote quotes s for a POSIX shell: wrapped in single quotes, with any
// embedded single quote closed, escaped, and reopened. It is the only quoting a `run:`
// line's own arguments need, since nothing inside single quotes is expanded.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SC-101, FR-101: two processes sharing one backend, one tick, exactly one run. The
// effect counted is the line the run appends, never a line claiming a claim was held.
//
// Both hosts are started concurrently and armed against one shared deadline, and the
// schedule is pinned to a single instant computed from it — the same fix #56 gave
// TestADelayedHostDoesNotRunATickAgain. Starting them one after another let a loaded
// runner arm the second host after the wildcard schedule's tick had already been taken
// by the first; the second's next occurrence was then a minute away and nothing ever
// collided, which is exactly "neither host recorded a claim_held refusal" from CI run
// 34925617933.
//
// Pinning the instant is not enough by itself: the loser's request also has to land
// while the winner's claim is still held, or internal/guard/etcd's Acquire reads the
// claim gone and refuses it as tick_already_ran instead — its tickRan check cannot tell
// the two apart once the claim key is gone. So the winning host's gather step does not
// leave its mark on its own timing: it waits for a file the test creates, and the test
// creates it only once a claim_held refusal is on record, which makes that refusal a
// precondition of the claim ever being released rather than a race against it.
func TestTwoServesRunOneTickOnce(t *testing.T) {
	t.Setenv(fakeagent.ModeVar, fakeagent.ModeSuccess)
	// Built before the deadline is taken, so that compiling is not charged to arming.
	bintest.Build(t)
	fakeagent.Build(t)

	armedBy := time.Now().UTC().Add(armWithin)
	// Both hosts must be armed, with margin, before the tick they will race for falls
	// due (#56's minute computation).
	at := armedBy.Add(10 * time.Second).Truncate(time.Minute).Add(time.Minute)

	release := filepath.Join(t.TempDir(), "release")
	// maxReleaseWait bounds the wait in tenths of a second (30s), so a run whose test
	// never creates the release file fails on its own rather than riding gather's own
	// two-minute step timeout.
	const maxReleaseWait = 300
	before := fmt.Sprintf(
		`i=0; while [ ! -e %s ]; do i=$((i+1)); [ "$i" -ge %d ] && exit 1; sleep 0.1; done`,
		shellSingleQuote(release), maxReleaseWait)
	c := newCluster(t,
		fmt.Sprintf("%d %d %d %d *", at.Minute(), at.Hour(), at.Day(), int(at.Month())), before)

	host0 := c.start(t, 0)
	host1 := c.start(t, 1)
	host0.Expect(t, "armed 1 schedule", time.Until(armedBy))
	host1.Expect(t, "armed 1 schedule", time.Until(armedBy))
	if now := time.Now().UTC(); !now.Before(at) {
		t.Fatalf("both hosts were armed at %s, after the tick %s fell due", now, at)
	}

	var refused string
	waitFor(t, time.Until(at)+30*time.Second, func() bool {
		refused = c.refusals(t, 0) + c.refusals(t, 1)
		return strings.Contains(refused, string(record.MechanismClaimHeld))
	}, func() string {
		return "neither host recorded a claim_held refusal:\n" + refused
	})
	// Naming the run that holds the claim is what makes the refusal actionable: an
	// identifier an operator can look up with `gronin show`.
	if !strings.Contains(refused, "run 20") {
		t.Fatalf("the refusal does not name the run holding the claim:\n%s", refused)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	c.waitForRuns(t, 1, 30*time.Second)
	// And stays one: the host that was refused must not run it a moment later.
	time.Sleep(5 * time.Second)
	if ran := c.ran(t); ran != 1 {
		t.Fatalf("%d runs of one tick", ran)
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
