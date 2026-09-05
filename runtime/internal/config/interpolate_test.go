package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/config"
)

func configWith(t *testing.T, values map[string]config.Value) *config.Config {
	t.Helper()
	c, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range values {
		if err := c.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestInterpolateResolvesBothSources(t *testing.T) {
	c := configWith(t, map[string]config.Value{"channel": {Value: "#ops"}})

	got, err := c.Interpolate("to ${config.channel} about ${trigger.alert}",
		map[string]string{"alert": "DiskFull"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "to #ops about DiskFull"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// FR-008. This is the test the constitution asks for by name: the resolver must not see
// the process environment even when the environment holds exactly the name being asked
// for. Written as a positive requirement on the resolver, because that is what it is.
func TestInterpolateNeverReadsTheProcessEnvironment(t *testing.T) {
	const name = "GRONIN_TEST_LEAK"
	t.Setenv(name, "the-environment-value")

	c := configWith(t, nil)

	for _, text := range []string{
		"${" + name + "}",
		"${config." + name + "}",
		"${trigger." + name + "}",
		"${env." + name + "}",
	} {
		got, err := c.Interpolate(text, nil)
		if err == nil {
			t.Errorf("%s resolved to %q; the environment is not a source", text, got)
		}
		if strings.Contains(got, "the-environment-value") {
			t.Errorf("%s leaked the environment value", text)
		}
	}

	// And nothing the environment holds at all, not just that name.
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if value == "" {
			continue
		}
		if got, err := c.Interpolate("${config."+name+"}", nil); err == nil {
			t.Fatalf("${config.%s} resolved to %q from the environment", name, got)
		}
	}
}

func TestInterpolateRefusesAReferenceThatDoesNotNameItsSource(t *testing.T) {
	c := configWith(t, map[string]config.Value{"channel": {Value: "#ops"}})

	_, err := c.Interpolate("${channel}", map[string]string{"channel": "#attacker"})
	if err == nil {
		t.Fatal("a bare reference resolved; a trigger payload could then shadow a configured value")
	}
	for _, want := range []string{"does not name its source", "${config.channel}", "${trigger.channel}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

func TestInterpolateReportsEveryProblem(t *testing.T) {
	c := configWith(t, nil)

	_, err := c.Interpolate("${config.one} ${trigger.two} ${three} ${nope.four}", nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"${config.one}", "${trigger.two}", "${three}", "nope"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("only some problems were reported; %q is missing from: %v", want, err)
		}
	}
}

func TestInterpolateLeavesTextWithNoReferencesAlone(t *testing.T) {
	c := configWith(t, nil)

	const text = "a $ and a { and a } walk into a $bar"
	got, err := c.Interpolate(text, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != text {
		t.Fatalf("got %q, want it unchanged", got)
	}
}

// The two malformed forms the regex alone did not answer for.
func TestInterpolateRefusesAMalformedReference(t *testing.T) {
	c := configWith(t, map[string]config.Value{"x": {Value: "resolved"}})

	t.Run("a reference that never closes", func(t *testing.T) {
		// It matches nothing, so without a check of its own it passes through as literal
		// text — the one malformed form that resolves rather than being refused.
		for _, text := range []string{"${config.x", "a ${config.x and a tail", "${"} {
			if got, err := c.Interpolate(text, nil); err == nil {
				t.Errorf("%q was accepted and produced %q", text, got)
			}
		}
	})

	t.Run("a reference nested in another", func(t *testing.T) {
		_, err := c.Interpolate("${config.${trigger.x}}", map[string]string{"x": "x"})
		if err == nil {
			t.Fatal("a nested reference was accepted")
		}
		if !strings.Contains(err.Error(), "nests a reference") {
			t.Errorf("the refusal reads as something else: %v", err)
		}
	})

	t.Run("a closing brace on its own is text", func(t *testing.T) {
		const text = "} and { and $ walk into a bar"
		got, err := c.Interpolate(text, nil)
		if err != nil || got != text {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
}

// A resolved value that itself looks like a reference is data, not a template. Resolving
// it again would let a configured value reach into the trigger payload, or the reverse.
func TestInterpolateDoesNotResolveWhatItJustResolved(t *testing.T) {
	c := configWith(t, map[string]config.Value{
		"template": {Value: "${trigger.secret}"},
	})

	got, err := c.Interpolate("${config.template}", map[string]string{"secret": "leaked"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "${trigger.secret}" {
		t.Fatalf("got %q; a resolved value was resolved a second time", got)
	}
}
