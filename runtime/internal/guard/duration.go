package guard

import (
	"strconv"
	"strings"
	"time"
)

// HumanDuration is d rounded to the second and written as contracts/cli.md writes a
// duration: unit by unit, largest first, leaving out every unit that is zero — 30m, 12m14s,
// 2h5s — and 0s for less than half a second.
func HumanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d <= 0 {
		return "0s"
	}
	var text strings.Builder
	for _, part := range []struct {
		count time.Duration
		unit  string
	}{{d / time.Hour, "h"}, {d % time.Hour / time.Minute, "m"}, {d % time.Minute / time.Second, "s"}} {
		if part.count > 0 {
			text.WriteString(strconv.FormatInt(int64(part.count), 10) + part.unit)
		}
	}
	return text.String()
}
