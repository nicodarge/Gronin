package main

import (
	"testing"
	"time"

	"github.com/nicodarge/Gronin/runtime/internal/guard"
)

// contracts/cli.md's waiting line, with the holder the coordinator named and without one.
func TestTheWaitingLine(t *testing.T) {
	for name, probe := range map[string]struct {
		waiting guard.Waiting
		want    string
	}{
		"a named holder": {
			waiting: guard.Waiting{
				Holder: &guard.Holder{
					RunID: "20260910T060000Z-3f9a1c0b2e4d", Host: "host-b.example.com", Instance: "8f2c1a0b",
				},
				UpTo: 30 * time.Minute,
			},
			want: "doc-check is running (run 20260910T060000Z-3f9a1c0b2e4d on host-b.example.com, process 8f2c1a0b); waiting up to 30m",
		},
		"no holder named": {
			waiting: guard.Waiting{
				Held: "a run of this playbook is already in flight (claim held): held by another process",
				UpTo: 90 * time.Second,
			},
			want: "doc-check is running (held by another process); waiting up to 1m30s",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := waitingLine("doc-check", probe.waiting); got != probe.want {
				t.Fatalf("waiting line = %q, want %q", got, probe.want)
			}
		})
	}
}
