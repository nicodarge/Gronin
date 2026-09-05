package run

import (
	"strings"
	"testing"
	"time"
)

// The ordering half, which the manager-level test could not reach: fifty runs begun
// back to back all land in the same second, so comparing their prefixes never crosses a
// boundary and the assertion holds for a format that is wrong. These are fixed times,
// chosen to move each component of the stamp in turn.
func TestIdentifiersSortByTheTimeTheyCarry(t *testing.T) {
	moments := []time.Time{
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), // second
		time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC), // minute
		time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC), // hour
		time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), // day
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), // month
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), // year
		time.Date(2030, 12, 31, 23, 59, 59, 0, time.UTC),
	}

	var previous string
	for _, moment := range moments {
		id, err := newID(moment)
		if err != nil {
			t.Fatal(err)
		}
		if previous != "" && id <= previous {
			t.Fatalf("%s (%s) does not sort after %s", id, moment, previous)
		}
		previous = id
	}
}

// A stamp that is not the time it was given is a stamp an operator cannot read, and the
// ordering test above would not notice a component in the wrong place as long as it
// still increased.
func TestTheIdentifierCarriesTheTimeItWasGiven(t *testing.T) {
	id, err := newID(time.Date(2026, 9, 5, 14, 22, 33, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "20260905T142233Z-") {
		t.Fatalf("identifier = %q", id)
	}
}

func TestIdentifiersDoNotCollideAtTheSameInstant(t *testing.T) {
	moment := time.Date(2026, 9, 5, 14, 22, 33, 0, time.UTC)

	seen := map[string]bool{}
	for range 2000 {
		id, err := newID(moment)
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("identifier %q was issued twice for one instant", id)
		}
		seen[id] = true
	}
}
