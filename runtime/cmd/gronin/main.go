// Command gronin loads declarative playbooks, refuses the unsafe ones before arming
// anything, and drives one bounded agent per run.
//
// This is the Phase 1 skeleton: it reports its own version and nothing else.
package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	err := newRootCommand(os.Stdout, os.Stderr).Execute()
	if err == nil {
		return
	}
	// A refusal has already said everything it has to say, in the shape the contract
	// specifies. Repeating cobra's one-line summary underneath it buries the field the
	// author needs.
	var silent errSilent
	if !errors.As(err, &silent) {
		fmt.Fprintln(os.Stderr, "gronin:", err)
	}
	os.Exit(1)
}
