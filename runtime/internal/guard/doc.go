// Package guard decides, before any command runs and any token is spent, whether a trigger
// becomes a run. It holds the Coordinator interface every claim backend is held to
// (specs/002-guard/contracts/coordination.md), and the runtime's side of holding a claim.
//
// It will import record and playbook as the stage lands; it must never import run, which
// fences through it before every side effect, and would be a cycle the other way.
package guard
