package gather_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/stage/gather"
)

func options(t *testing.T) gather.Options {
	t.Helper()
	return gather.Options{WorkDir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin"}}
}

func TestStepsWriteWhatTheyProduce(t *testing.T) {
	opts := options(t)

	results, err := gather.Run(t.Context(), []gather.Step{
		{Run: "echo one", As: "one.txt"},
		{Run: "echo two", As: "two.txt"},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("%d results", len(results))
	}
	for _, result := range results {
		data, err := os.ReadFile(filepath.Join(opts.WorkDir, result.Name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Fatalf("%s is empty", result.Name)
		}
		if result.Bytes != int64(len(data)) {
			t.Fatalf("%s recorded %d bytes, wrote %d", result.Name, result.Bytes, len(data))
		}
	}
}

// FR-010. The whole point is that it costs nothing: a broken input aborts before the
// stage that spends money, and the steps after it do not run either.
func TestAFailingStepAbortsBeforeAnythingElseRuns(t *testing.T) {
	opts := options(t)

	results, err := gather.Run(t.Context(), []gather.Step{
		{Run: "echo first", As: "first.txt"},
		{Run: "echo to stderr >&2; exit 3", As: "second.txt"},
		{Run: "echo third", As: "third.txt"},
	}, opts)

	if !errors.Is(err, gather.ErrStepFailed) {
		t.Fatalf("err = %v, want ErrStepFailed", err)
	}
	if len(results) != 2 {
		t.Fatalf("%d steps ran; the third should not have", len(results))
	}
	if results[1].ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", results[1].ExitCode)
	}
	if !strings.Contains(string(results[1].Stderr), "to stderr") {
		t.Fatalf("stderr = %q; a failing step's stderr is why the run was refused", results[1].Stderr)
	}
	if _, err := os.Stat(filepath.Join(opts.WorkDir, "third.txt")); !os.IsNotExist(err) {
		t.Fatal("the step after the failure ran")
	}
	// What the earlier steps produced is still there: it is what an operator reads when
	// asking why the run never reached the agent.
	if results[0].ExitCode != 0 || results[0].Bytes == 0 {
		t.Fatalf("the successful step was lost: %+v", results[0])
	}
}

func TestOutputPastTheLimitIsTruncatedAndSaidSo(t *testing.T) {
	opts := options(t)
	opts.MaxBytes = 64

	results, err := gather.Run(t.Context(), []gather.Step{
		{Run: "head -c 4096 /dev/zero | tr '\\0' 'x'", As: "big.txt"},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}

	result := results[0]
	if !result.Truncated {
		t.Fatal("4 KiB written under a 64 byte limit was not recorded as truncated")
	}
	if result.Bytes != 64 {
		t.Fatalf("recorded %d bytes, want 64", result.Bytes)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 64 {
		t.Fatalf("%d bytes on disk, want 64", len(data))
	}
}

func TestOutputUnderTheLimitIsNotCalledTruncated(t *testing.T) {
	opts := options(t)
	opts.MaxBytes = 64

	results, err := gather.Run(t.Context(), []gather.Step{{Run: "echo small", As: "small.txt"}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Truncated {
		t.Fatal("six bytes under a 64 byte limit was called truncated")
	}
}

// The environment a step runs with is the one it was given, not the runtime's. A gather
// command is written by a playbook author, and this process holds the deployment's
// credentials.
func TestAStepDoesNotInheritTheRuntimeEnvironment(t *testing.T) {
	t.Setenv("GRONIN_TEST_CREDENTIAL", "must-not-reach-a-step")
	opts := options(t)

	results, err := gather.Run(t.Context(), []gather.Step{
		{Run: "env", As: "env.txt"},
	}, opts)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(results[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "must-not-reach-a-step") {
		t.Fatal("a gather step was handed the runtime's environment")
	}
}

func TestAStepMayNotWriteOutsideTheWorkingDirectory(t *testing.T) {
	opts := options(t)

	for _, name := range []string{"../escape", "/etc/passwd", "nested/file", ".hidden"} {
		if _, err := gather.Run(t.Context(), []gather.Step{{Run: "echo x", As: name}}, opts); err == nil {
			t.Errorf("a step writing %q was allowed", name)
		}
	}
}

func TestTwoStepsMayNotWriteTheSameName(t *testing.T) {
	opts := options(t)

	_, err := gather.Run(t.Context(), []gather.Step{
		{Run: "echo one", As: "same.txt"},
		{Run: "echo two", As: "same.txt"},
	}, opts)
	if err == nil {
		t.Fatal("the second step silently replaced the first step's output")
	}
}

// Without a bound a hanging step holds the playbook's claim forever and never reaches
// the timeout that belongs to the agent stage.
func TestAHangingStepIsKilled(t *testing.T) {
	opts := options(t)
	opts.StepTimeout = 150 * time.Millisecond

	started := time.Now()
	results, err := gather.Run(t.Context(), []gather.Step{{Run: "sleep 30", As: "slow.txt"}}, opts)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a step that sleeps thirty seconds finished cleanly")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the step ran for %v; the timeout did not apply", elapsed)
	}
	if len(results) != 1 {
		t.Fatalf("%d results", len(results))
	}
}

func TestStepsRunInsideTheWorkingDirectory(t *testing.T) {
	opts := options(t)

	results, err := gather.Run(t.Context(), []gather.Step{{Run: "pwd", As: "pwd.txt"}}, opts)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(results[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(opts.WorkDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("a step ran in %q, want %q", got, want)
	}
}
