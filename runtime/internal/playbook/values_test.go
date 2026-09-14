package playbook_test

import (
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/playbook"
)

// bookWith is a webhook playbook declaring one value named "alertname", for Check's own
// corpus. The gather step is added only by the cases that need a value bound into it
// (T019's dash rule).
func bookWith(value playbook.TriggerValue, gather ...playbook.Step) *playbook.Playbook {
	return bookWithNamed("alertname", value, gather...)
}

func bookWithNamed(name string, value playbook.TriggerValue, gather ...playbook.Step) *playbook.Playbook {
	return &playbook.Playbook{
		Name: "alert-triage",
		Trigger: playbook.Trigger{
			Type: "webhook", Source: "alerts",
			Values: map[string]playbook.TriggerValue{name: value},
		},
		Gather: gather,
	}
}

// SC-317 and SC-319. Every shape Check must refuse, and every one it must accept, from a
// body extracted with Extract — the pair this feature guarantees, per tasks.md's "every
// test asserting an absence asserts the positive case beside it".
func TestDeclaredValuesAreHeldToTheirDeclaredShape(t *testing.T) {
	value := playbook.TriggerValue{At: "/alertname", Pattern: "[a-z ]+", MaxLength: 10}
	book := bookWith(value)

	refusedBody := func(t *testing.T, body string) []playbook.ValueRefusal {
		t.Helper()
		extracted := playbook.Extract(book.Trigger, []byte(body))
		_, refusals := playbook.Check(book, extracted)
		if len(refusals) == 0 {
			t.Fatalf("%s was accepted", body)
		}
		return refusals
	}

	for name, probe := range map[string]struct {
		body string
		kind playbook.ValueRefusalKind
	}{
		"absent":                  {`{"other":"x"}`, playbook.ValueAbsent},
		"null":                    {`{"alertname":null}`, playbook.ValueAbsent},
		"an object":               {`{"alertname":{"a":1}}`, playbook.ValueNotSingle},
		"an array":                {`{"alertname":["a"]}`, playbook.ValueNotSingle},
		"one code point over":     {`{"alertname":"disk fullxx"}`, playbook.ValueTooLong},
		"a partial pattern match": {`{"alertname":"disk full!"}`, playbook.ValueNoMatch},
	} {
		t.Run(name, func(t *testing.T) {
			refusals := refusedBody(t, probe.body)
			if refusals[0].Name != "alertname" {
				t.Fatalf("the refusal does not name the value: %+v", refusals[0])
			}
			if refusals[0].Kind != probe.kind {
				t.Fatalf("kind = %s, want %s: %v", refusals[0].Kind, probe.kind, refusals[0].Problem)
			}
			if !strings.Contains(refusals[0].Problem.Field, book.Name) {
				t.Fatalf("the refusal does not name the playbook: %s", refusals[0].Problem.Field)
			}
		})
	}

	// One code point over its length in multi-byte code points is refused the same way
	// a single-byte one is, and exactly at the length is accepted — the length is
	// counted in Unicode code points, not bytes.
	multiByte := playbook.TriggerValue{At: "/alertname", Pattern: ".+", MaxLength: 4}
	multiByteBook := bookWith(multiByte)
	over := playbook.Extract(multiByteBook.Trigger, []byte(`{"alertname":"cafés"}`))
	if _, refusals := playbook.Check(multiByteBook, over); len(refusals) == 0 {
		t.Fatal("cafés (5 code points) at a limit of 4 was accepted")
	}
	extracted := playbook.Extract(multiByteBook.Trigger, []byte(`{"alertname":"café"}`))
	checked, refusals := playbook.Check(multiByteBook, extracted)
	if len(refusals) != 0 {
		t.Fatalf("café (4 code points, 5 bytes) at a limit of 4 was refused: %v", refusals)
	}
	if checked["alertname"] != "café" {
		t.Fatalf("checked = %+v", checked)
	}

	// A string, a boolean and a number are single values, and a number is kept as the
	// literal the body held — never re-rendered from a decoded float, so a value one
	// digit past 2^53 stays itself.
	acceptedShapes := playbook.TriggerValue{At: "/v", Pattern: ".+", MaxLength: 40}
	shapesBook := bookWith(acceptedShapes)
	for name, body := range map[string]string{
		"string":  `{"v":"disk full"}`,
		"boolean": `{"v":true}`,
		"number":  `{"v":9007199254740993}`,
	} {
		t.Run("accepted "+name, func(t *testing.T) {
			extracted := playbook.Extract(shapesBook.Trigger, []byte(body))
			checked, refusals := playbook.Check(shapesBook, extracted)
			if len(refusals) != 0 {
				t.Fatalf("%s was refused: %v", body, refusals)
			}
			if name == "number" && checked["alertname"] != "9007199254740993" {
				t.Fatalf("the number was re-rendered as %q", checked["alertname"])
			}
		})
	}
}

// The pattern is anchored: a value matching it only in part is refused, not accepted as
// an unanchored regexp would.
func TestAPartialPatternMatchIsRefused(t *testing.T) {
	book := bookWith(playbook.TriggerValue{At: "/v", Pattern: "ok", MaxLength: 20})
	extracted := playbook.Extract(book.Trigger, []byte(`{"v":"not ok at all"}`))
	if _, refusals := playbook.Check(book, extracted); len(refusals) == 0 {
		t.Fatal("a value matching its pattern only in part was accepted")
	}
}

// SC-319. A declared value beginning with a dash is refused only when a gather step
// references it — accepted when nothing binds it into a command, refused when something
// does, because that is the only path through which it could become an option.
func TestALeadingDashIsRefusedOnlyWhenAGatherStepBindsIt(t *testing.T) {
	value := playbook.TriggerValue{At: "/v", Pattern: ".+", MaxLength: 40}

	unbound := bookWithNamed("v", value)
	extracted := playbook.Extract(unbound.Trigger, []byte(`{"v":"--rm"}`))
	if _, refusals := playbook.Check(unbound, extracted); len(refusals) != 0 {
		t.Fatalf("a leading dash with no gather step to reach was refused: %v", refusals)
	}

	bound := bookWithNamed("v", value, playbook.Step{Run: "echo ${trigger.v}", As: "out.txt"})
	extracted = playbook.Extract(bound.Trigger, []byte(`{"v":"--rm"}`))
	_, refusals := playbook.Check(bound, extracted)
	if len(refusals) == 0 {
		t.Fatal("a leading dash bound into a gather step was accepted")
	}
	if refusals[0].Kind != playbook.ValueLeadingDash || refusals[0].Name != "v" {
		t.Fatalf("refusals = %+v", refusals)
	}
}

// Check applies the same rule to a manual invocation's --trigger strings: a name the
// caller did not supply is exactly as absent as one whose declared pointer resolved to
// nothing, and every other refusal reads the same way on both paths.
func TestCheckAppliesTheSameRuleToManualTriggerStrings(t *testing.T) {
	book := bookWith(playbook.TriggerValue{At: "/alertname", Pattern: "[a-z ]+", MaxLength: 10})

	asAny := func(values map[string]string) map[string]any {
		out := make(map[string]any, len(values))
		for k, v := range values {
			out[k] = v
		}
		return out
	}

	if _, refusals := playbook.Check(book, asAny(map[string]string{})); len(refusals) == 0 ||
		refusals[0].Kind != playbook.ValueAbsent {
		t.Fatalf("an undeclared --trigger value was not refused as absent: %v", refusals)
	}

	checked, refusals := playbook.Check(book, asAny(map[string]string{"alertname": "disk full"}))
	if len(refusals) != 0 || checked["alertname"] != "disk full" {
		t.Fatalf("checked = %v, refusals = %v", checked, refusals)
	}

	if _, refusals := playbook.Check(book, asAny(map[string]string{"alertname": "DISK FULL"})); len(refusals) == 0 ||
		refusals[0].Kind != playbook.ValueNoMatch {
		t.Fatalf("a --trigger value failing its pattern was not refused for that: %v", refusals)
	}
}

// Extract never fails: a body that is not JSON, or has no object at its root, leaves
// every declared value absent rather than erroring.
func TestExtractNeverFails(t *testing.T) {
	book := bookWith(playbook.TriggerValue{At: "/alertname", Pattern: ".+", MaxLength: 10})
	for name, body := range map[string]string{
		"not JSON":      "not json at all",
		"a JSON array":  `["a","b"]`,
		"a JSON scalar": `42`,
		"empty":         "",
	} {
		t.Run(name, func(t *testing.T) {
			extracted := playbook.Extract(book.Trigger, []byte(body))
			_, refusals := playbook.Check(book, extracted)
			if len(refusals) == 0 || refusals[0].Kind != playbook.ValueAbsent {
				t.Fatalf("%q did not read as absent: %v", body, refusals)
			}
		})
	}
}
