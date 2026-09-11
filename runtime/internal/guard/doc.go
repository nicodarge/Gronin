// Package guard decides, before any command runs and any token is spent, whether a trigger
// becomes a run. It holds the Coordinator interface every claim backend is held to
// (specs/002-guard/contracts/coordination.md), and the runtime's side of holding a claim.
//
// It imports record and playbook, never run: run fences through it before every side
// effect, and the reverse import would be a cycle.
package guard
