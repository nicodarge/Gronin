package record_test

import (
	"strings"
	"testing"

	"github.com/nicodarge/Gronin/runtime/internal/record"
)

func TestRedactorReplacesEveryOccurrence(t *testing.T) {
	r := record.NewRedactor([]string{"t0ken"})

	got := r.Redact("sent t0ken, retried with t0ken")
	if strings.Contains(got, "t0ken") {
		t.Fatalf("a secret survived: %q", got)
	}
	if want := "sent [redacted], retried with [redacted]"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The case that made the ordering deliberate.
func TestRedactorLeavesNoFragmentOfAContainingSecret(t *testing.T) {
	const token = "t0ken"
	const url = "https://example.com/hook/" + token

	r := record.NewRedactor([]string{token, url})

	got := r.Redact("posting to " + url)
	if strings.Contains(got, "example.com/hook") {
		t.Fatalf("the shorter secret was replaced first and left the rest behind: %q", got)
	}
	if want := "posting to [redacted]"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRedactorIgnoresAnEmptySecret(t *testing.T) {
	r := record.NewRedactor([]string{"", "t0ken"})

	const text = "nothing to hide"
	if got := r.Redact(text); got != text {
		t.Fatalf("an empty secret rewrote the text: %q", got)
	}
}

func TestNilRedactorIsUsable(t *testing.T) {
	var r *record.Redactor
	const text = "a deployment that configured no secrets"
	if got := r.Redact(text); got != text {
		t.Fatalf("got %q", got)
	}
	if got := string(r.RedactBytes([]byte(text))); got != text {
		t.Fatalf("got %q", got)
	}
}
