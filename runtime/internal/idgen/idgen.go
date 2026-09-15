// Package idgen mints the identifier shape a run and a delivery both use, so the blob
// store files either kind of record's artifacts the same way. A leaf package with no
// internal imports: internal/run and internal/record both need it, and record cannot
// import run (run imports record).
package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// New mints an identifier that sorts by the time it carries and cannot collide: the
// timestamp is the readable half, so an operator can tell which record is which without
// a lookup, and the random half is what makes it an identifier — two records starting in
// the same nanosecond is not a scenario worth a coordination protocol, but it is one
// worth six bytes.
func New(at time.Time) (string, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generating an identifier: %w", err)
	}
	return at.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix), nil
}
