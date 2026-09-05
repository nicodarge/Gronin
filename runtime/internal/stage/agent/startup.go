package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nicodarge/Gronin/runtime/internal/proc"
)

// VersionFloor is the version the bounding flags were confirmed on.
//
// A pinned version in code rather than in prose, because it is a decision this runtime
// enforces rather than a description of something else. FR-019 is the reason: a flag an
// older executable does not recognise is IGNORED rather than refused, so a bound
// expressed as a flag fails open, and a run would look bounded while being unbounded.
// Raising it is a deliberate act, and the flags should be re-confirmed when it is.
const VersionFloor = "2.1.261"

// ErrBelowVersionFloor is what refuses to start against an executable too old to know
// the flags this runtime relies on.
var ErrBelowVersionFloor = errors.New("the agent executable is older than the version its bounding flags were confirmed on")

// Version asks the executable what it is.
func Version(ctx context.Context, executable string) (string, error) {
	cmd := exec.CommandContext(ctx, executable, "--version") //nolint:gosec // the configured executable
	proc.Isolate(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("asking %s for its version: %w", executable, err)
	}
	// "2.1.261 (Claude Code)" — the number is the first field.
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return "", fmt.Errorf("%s reported no version", executable)
	}
	return fields[0], nil
}

// RefuseBelowFloor compares a version against the floor without running anything, so the
// comparison itself can be probed on the values that matter — "2.1.9" is older than
// "2.1.10" and sorts after it as a string.
func RefuseBelowFloor(version string) error {
	below, err := isBelow(version, VersionFloor)
	if err != nil {
		return err
	}
	if below {
		return fmt.Errorf("%w: %s, and the floor is %s", ErrBelowVersionFloor, version, VersionFloor)
	}
	return nil
}

// CheckVersion refuses an executable below the floor.
func CheckVersion(ctx context.Context, executable string) (string, error) {
	version, err := Version(ctx, executable)
	if err != nil {
		return "", err
	}
	if err := RefuseBelowFloor(version); err != nil {
		return version, fmt.Errorf("%s: %w", executable, err)
	}
	return version, nil
}

// isBelow compares two dotted numeric versions. Numeric per component, not lexical:
// "2.1.9" is not above "2.1.10", and a string comparison says it is.
func isBelow(version, floor string) (bool, error) {
	got, err := parts(version)
	if err != nil {
		return false, err
	}
	want, err := parts(floor)
	if err != nil {
		return false, err
	}
	for at := range max(len(got), len(want)) {
		g, w := at2(got, at), at2(want, at)
		if g != w {
			return g < w, nil
		}
	}
	return false, nil
}

func parts(version string) ([]int, error) {
	// A pre-release or build suffix is not part of the ordering this needs.
	version, _, _ = strings.Cut(version, "-")
	version, _, _ = strings.Cut(version, "+")

	var out []int
	for _, field := range strings.Split(version, ".") {
		number, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("%q is not a version this runtime can compare", version)
		}
		out = append(out, number)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%q is not a version", version)
	}
	return out, nil
}

func at2(values []int, at int) int {
	if at < len(values) {
		return values[at]
	}
	return 0
}

// ErrNoCredential is what refuses to start when the agent process reports that it found
// no credential.
var ErrNoCredential = errors.New("the agent process found no credential")

// CredentialSources are the places the executable's own documentation says a credential
// can come from. The runtime does not reimplement the resolution — their order is the
// CLI's and was deliberately not established — it names them in a refusal so an operator
// knows where to look.
var CredentialSources = []string{
	"ANTHROPIC_API_KEY",
	"an apiKeyHelper in a settings file",
	"an OAuth session",
	"a keychain entry",
	"a cloud provider's own credentials (Bedrock, Vertex, Foundry)",
}

// VerifyCredential asks the agent process which credential source it used, and refuses
// when it reports none (FR-032, FR-033).
//
// It reads the answer off the process's own first event rather than asserting one:
// research established which sources exist and, just as importantly, that their
// resolution order is not documented. The run is aborted at that first event, before the
// model is asked anything.
func VerifyCredential(ctx context.Context, executable string, env []string, workDir string) (string, error) {
	stop := errors.New("the receipt has been read")
	var source string

	outcome, err := Run(ctx, Declaration{Restricted: true}, Options{
		Executable: executable,
		WorkDir:    workDir,
		Prompt:     "hello",
		Env:        env,
		OnEvent: func(event Event) error {
			if event.Type == "system" && event.Subtype == "init" {
				source = event.APIKeySource
				return stop
			}
			return nil
		},
	})
	if err != nil {
		return "", err
	}
	if outcome.Aborted == nil && outcome.Stream.Init == nil {
		return "", fmt.Errorf("%s produced no startup event; its stderr was: %s",
			executable, strings.TrimSpace(string(outcome.Stderr)))
	}
	if source == "" || source == "none" {
		return "", fmt.Errorf("%w; it consults: %s",
			ErrNoCredential, strings.Join(CredentialSources, ", "))
	}
	return source, nil
}
