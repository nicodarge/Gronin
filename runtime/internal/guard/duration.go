package guard

import (
	"strings"
	"time"
)

// TimeOfDay is how a refusal's detail names an instant: its date is already in the line's
// own timestamp (contracts/cli.md).
const TimeOfDay = "15:04:05Z"

// HumanDuration is d rounded to the second and written as contracts/cli.md writes one:
// 30m and 12m14s, never 30m0s.
func HumanDuration(d time.Duration) string {
	text := d.Round(time.Second).String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return text
}
