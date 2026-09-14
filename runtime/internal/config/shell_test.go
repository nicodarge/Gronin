package config_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/config"
)

// The value a deployment holds is data the playbook cannot see, so the containment has to
// hold for any byte string. Each of these is a value that, substituted into the command's
// text, would run something the playbook never declared.
//
// Driven through both namespaces rather than only ${config.x}. They bind through the same
// mechanism, and a trigger payload is the one this repository elsewhere describes as
// written by whoever sent the request — so it is the half that most needs probing, and it
// was the half with no coverage.
func TestABoundValueCannotBecomeACommand(t *testing.T) {
	hostile := []string{
		"; touch pwned",
		"$(touch pwned)",
		"`touch pwned`",
		"&& touch pwned",
		"| touch pwned",
		"\n touch pwned",
		"' ; touch pwned ; '",
		"$(echo hi)",
		"*",
		"~",
		"a b c",
	}

	for _, namespace := range []string{"config", "trigger"} {
		for _, value := range hostile {
			t.Run(namespace+"/"+value, func(t *testing.T) {
				var (
					cfg     *config.Config
					trigger map[string]string
				)
				if namespace == "config" {
					cfg = configWith(t, map[string]config.Value{"target": {Value: value}})
				} else {
					cfg = configWith(t, nil)
					trigger = map[string]string{"target": value}
				}

				bound, err := cfg.BindShell("printf %s ${"+namespace+".target}", trigger)
				if err != nil {
					t.Fatalf("binding refused a legitimate line: %v", err)
				}

				// Run it. A claim that a quoting scheme contains something is exactly the
				// claim that is wrong when it is only read.
				cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", bound.Line)
				cmd.Env = bound.Env
				cmd.Dir = t.TempDir()
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("the bound line did not run: %v", err)
				}
				if string(out) != value {
					t.Errorf("the step saw %q, and the value is %q", out, value)
				}
			})
		}
	}
}

// A reference inside quotes is refused rather than bound: inside single quotes the
// expansion would not happen and the author would get the literal text, and inside double
// quotes it would nest and split on whitespace. Both are silent, which is why they are
// refused instead.
func TestAReferenceInsideQuotesIsRefused(t *testing.T) {
	cfg := configWith(t, map[string]config.Value{"glob": {Value: "docs/*.md"}})
	for _, line := range []string{
		"git ls-files '${config.glob}'",
		`git ls-files "${config.glob}"`,
		`echo "a ${config.glob} b"`,
		"echo 'x' '${config.glob}'",
	} {
		if _, err := cfg.BindShell(line, nil); err == nil {
			t.Errorf("%q was bound, and a quoted reference is meant to be refused", line)
		} else if !strings.Contains(err.Error(), "sits inside quotes") {
			t.Errorf("%q was refused, but not for its reason: %v", line, err)
		}
	}
}

// The accepting half. A denylist probed only on its refusals is an allowlist in disguise.
func TestAReferenceOutsideQuotesIsBound(t *testing.T) {
	cfg := configWith(t, map[string]config.Value{"glob": {Value: "docs/*.md"}})
	for _, line := range []string{
		"git ls-files ${config.glob}",
		"echo 'a quoted word' ${config.glob}",
		`echo "a quoted word" ${config.glob}`,
		`echo \' ${config.glob}`,
		"echo ${config.glob} ${config.glob}",
	} {
		if _, err := cfg.BindShell(line, nil); err != nil {
			t.Errorf("%q was refused: %v", line, err)
		}
	}
}

func TestAnUnresolvedReferenceIsRefused(t *testing.T) {
	cfg := configWith(t, map[string]config.Value{"known": {Value: "x"}})
	_, err := cfg.BindShell("echo ${config.missing}", nil)
	if err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("err = %v", err)
	}
}

// Two keys differing only by a dash would bind to one variable, and one of them would
// silently win.
func TestKeysThatCollideAsVariablesAreRefused(t *testing.T) {
	cfg := configWith(t, map[string]config.Value{
		"a-b": {Value: "first"}, "a_b": {Value: "second"},
	})
	_, err := cfg.BindShell("echo ${config.a-b} ${config.a_b}", nil)
	if err == nil || !strings.Contains(err.Error(), "both bind to") {
		t.Fatalf("err = %v", err)
	}
}

// TriggerReferences names only the payload values a line references, beside a
// ${config.x} on the same line it must not be confused with, and reports a name twice
// when the line reaches for it twice — Check applies FR-326's dash rule once per
// declaration, not once per line, and a caller that deduplicated here would undercount.
func TestTriggerReferencesNamesEveryPayloadReferenceOnTheLine(t *testing.T) {
	for name, probe := range map[string]struct {
		line string
		want []string
	}{
		"none":                       {"echo hello", nil},
		"only config":                {"echo ${config.b}", nil},
		"one":                        {"echo ${trigger.a}", []string{"a"}},
		"trigger beside config":      {"echo ${trigger.a} ${config.b}", []string{"a"}},
		"config beside trigger":      {"echo ${config.b} ${trigger.a}", []string{"a"}},
		"the same name twice":        {"echo ${trigger.a} ${trigger.a}", []string{"a", "a"}},
		"two different names":        {"echo ${trigger.a} ${trigger.b}", []string{"a", "b"}},
		"inside quotes, still named": {`echo "${trigger.a}"`, []string{"a"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := config.TriggerReferences(probe.line)
			if len(got) != len(probe.want) {
				t.Fatalf("got %v, want %v", got, probe.want)
			}
			for at, want := range probe.want {
				if got[at] != want {
					t.Fatalf("got %v, want %v", got, probe.want)
				}
			}
		})
	}
}
