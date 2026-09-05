// Command gronin loads declarative playbooks, refuses the unsafe ones before arming
// anything, and drives one bounded agent per run.
//
// This is the Phase 1 skeleton: it reports its own version and nothing else.
package main

import "os"

func main() {
	if err := newRootCommand(os.Stdout, os.Stderr).Execute(); err != nil {
		os.Exit(1)
	}
}
