package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

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

// ErrStartupProbeFailed is what refuses to start when the probe failed for a reason that
// is not the credential. Separate so an operator is not sent to check one.
var ErrStartupProbeFailed = errors.New("the agent process could not complete a startup turn")

// CredentialProbeTimeout bounds the startup turn. The stage default is sized for a
// playbook and is half an hour; a credential the API refuses drives the executable
// through its own retry ladder — about three minutes on 2.1.263, ten attempts each
// answered 401 — so without a bound of its own `gronin serve` hangs on a mistyped key.
//
// Deliberately shorter than that ladder. Waiting it out would classify the failure
// precisely and cost the operator three minutes to be told what a bounded refusal can
// tell them in one: the turn did not finish, and here is where a credential comes from.
const CredentialProbeTimeout = 60 * time.Second

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

// SourceNotNamed is what this reports when the run authenticated and the executable did
// not say from where. It is the ordinary answer for an OAuth session rather than an
// oddity, so an operator reading it should not go looking for a fault.
const SourceNotNamed = "not named by the agent"

// VerifyCredential asks the agent process to complete one trivial turn, and refuses when
// it could not (FR-032, FR-033).
//
// It used to read `apiKeySource` off the first event and refuse when that said "none".
// Measured against the shipped executable, that field says "none" for BOTH an authorised
// OAuth session and no credential at all — the same string, carrying no information. So
// the check refused every deployment authenticated the ordinary way: `gronin run` drove
// the agent and cost real money while `gronin serve` would not start beside it. Nothing
// caught it because the stub reports a source whenever one is configured, which the real
// process does not.
//
// What does separate them is the terminal event: unauthenticated, it comes back with
// is_error set and the assistant saying it is not logged in. Reading that means letting
// one turn finish, which is why the prompt is a word and the tool set is empty.
// The bound is a parameter rather than read from the constant here, so a test can prove
// the timeout path without waiting a minute for it. serve passes CredentialProbeTimeout.
func VerifyCredential(
	ctx context.Context, executable string, env []string, workDir string, within time.Duration,
) (string, error) {
	if within <= 0 {
		within = CredentialProbeTimeout
	}
	outcome, err := Run(ctx, Declaration{Restricted: true}, Options{
		Executable: executable,
		WorkDir:    workDir,
		Prompt:     "hello",
		Env:        env,
		Timeout:    within,
	})
	if err != nil {
		return "", err
	}
	// The bound, before anything reads the stream. A credential the API keeps refusing
	// drives the executable through its own retry ladder — measured at about three
	// minutes on 2.1.263 — so the commonest real failure arrives here rather than as a
	// classified terminal event, and saying "no terminal event" for it would name
	// neither the bound nor the likeliest cause.
	if outcome.TimedOut {
		return "", fmt.Errorf("%w: it did not finish one trivial turn within %s, which is "+
			"most often a credential the API keeps refusing; it consults: %s",
			ErrStartupProbeFailed, within, strings.Join(CredentialSources, ", "))
	}
	if outcome.Stream.Init == nil {
		return "", fmt.Errorf("%s produced no startup event; its stderr was: %s",
			executable, strings.TrimSpace(string(outcome.Stderr)))
	}
	if outcome.Stream.Result == nil {
		return "", fmt.Errorf("%s produced no terminal event; its stderr was: %s",
			executable, strings.TrimSpace(string(outcome.Stderr)))
	}
	if outcome.Stream.Result.IsError {
		// is_error alone does not mean unauthenticated — it is set for any failed turn,
		// a rate limit and a bad minute upstream included. Refusing all of those with
		// "found no credential" sends an operator to check a credential that is fine.
		//
		// Nothing logged in comes back with api_error_status present and null, because
		// the executable never reached the API. A credential the API refused comes back
		// with 401. Both
		// are credential problems; anything else is not, and says so as itself.
		status := strings.Trim(string(outcome.Stream.Result.APIErrorStatus), `"`)
		if status == "" || status == "null" || status == "401" {
			return "", fmt.Errorf("%w; it consults: %s",
				ErrNoCredential, strings.Join(CredentialSources, ", "))
		}
		return "", fmt.Errorf("%w: the agent process answered with status %s",
			ErrStartupProbeFailed, status)
	}

	source := outcome.Stream.Init.APIKeySource
	if source == "" || source == "none" {
		return SourceNotNamed, nil
	}
	return source, nil
}
