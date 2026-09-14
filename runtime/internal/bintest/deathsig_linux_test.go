//go:build linux

package bintest

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/fakeagent"
)

const deathsigHelperEnv = "BINTEST_DEATHSIG_HELPER_STATE_DIR"

// A state directory with no playbook, which `serve` arms and then holds open.
func emptyDeployment(t *testing.T) string {
	t.Helper()
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "playbooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

func serveArgs(t *testing.T, stateDir string) []string {
	t.Helper()
	return []string{"serve", "--state-dir", stateDir, "--agent", fakeagent.Build(t),
		"--api-address", "127.0.0.1:0"}
}

// runningWith reports whether pid is a live process whose argument vector names stateDir.
// Matching the directory rather than the pid alone keeps a reused pid from counting, and a
// zombie has an empty command line, so a killed serve its new parent has not reaped yet
// does not count either.
func runningWith(pid int, stateDir string) bool {
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && bytes.Contains(cmdline, []byte(stateDir))
}

// Not a test on its own: TestAServeDiesWithTheTestThatStartedIt runs it as a child and
// kills it once it has said which serve it holds.
func TestHelperStartsServeThenBlocks(t *testing.T) {
	stateDir := os.Getenv(deathsigHelperEnv)
	if stateDir == "" {
		t.Skip("run by TestAServeDiesWithTheTestThatStartedIt")
	}
	process := Start(t, serveArgs(t, stateDir)...)
	process.Expect(t, "armed", time.Minute)
	if _, err := fmt.Fprintf(os.Stdout, "serving %d\n", process.cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	select {}
}

// A test binary killed from outside never runs its cleanups, which is how `gronin serve`
// processes were found reparented to init with their state directories already deleted.
func TestAServeDiesWithTheTestThatStartedIt(t *testing.T) {
	stateDir := emptyDeployment(t)

	helper := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperStartsServeThenBlocks$")
	// The helper's own builds land in a directory removed here, since nothing in a process
	// that is killed removes them.
	helper.Env = append(os.Environ(), deathsigHelperEnv+"="+stateDir, "TMPDIR="+t.TempDir())
	var helperStderr bytes.Buffer
	helper.Stderr = &helperStderr
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := start(helper); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })

	reported := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if line, found := strings.CutPrefix(scanner.Text(), "serving "); found {
				reported <- line
				return
			}
		}
		close(reported)
	}()
	var pid int
	select {
	case line, ok := <-reported:
		if !ok {
			_ = helper.Wait()
			t.Fatalf("the helper ended without reporting a serve; stderr = %q", helperStderr.String())
		}
		if pid, err = strconv.Atoi(line); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Minute):
		t.Fatal("the helper reported no serve within 5m")
	}
	t.Cleanup(func() {
		if runningWith(pid, stateDir) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if !runningWith(pid, stateDir) {
		t.Fatalf("pid %d is not a serve on %s before anything was killed; the check below would pass vacuously", pid, stateDir)
	}

	if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for runningWith(pid, stateDir) {
		if time.Now().After(deadline) {
			t.Fatalf("serve %d was still running 10s after the test process that started it was killed", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A caller whose goroutine returns still locked to its thread ends that thread; see start.
func TestAProcessOutlivesTheThreadThatStartedIt(t *testing.T) {
	args := serveArgs(t, emptyDeployment(t))
	Build(t)

	var process *Process
	for attempt := 1; process == nil; attempt++ {
		// The main thread is the exception: the runtime parks it instead of ending it, so a
		// goroutine that lands there proves nothing, and is left to park it for good.
		if attempt > 3 {
			t.Fatal("no attempt ran off the main thread")
		}
		tid := make(chan int, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			runtime.LockOSThread()
			id := syscall.Gettid()
			tid <- id
			if id == os.Getpid() {
				return
			}
			process = Start(t, args...)
		}()
		// Start may call t.Fatal on that goroutine. It is safe only while the test function
		// waits here for it to end, and then returns on a nil process.
		<-done
		starter := <-tid
		if starter == os.Getpid() {
			continue
		}
		if process == nil {
			return
		}
		task := "/proc/self/task/" + strconv.Itoa(starter)
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(task); os.IsNotExist(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("thread %d that started serve had not exited within 10s; nothing below would be tested", starter)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	process.Expect(t, "armed", time.Minute)
	if code, err := process.Wait(2 * time.Second); err == nil {
		t.Fatalf("serve exited %d once the thread that started it had exited; stderr = %q", code, process.Stderr())
	}
}
