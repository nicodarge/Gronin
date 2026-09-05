package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrReceiptMismatch is what aborts a run whose child reported bounds wider than the
// playbook declared.
var ErrReceiptMismatch = errors.New("the agent process received a wider bound than the playbook declared")

// CheckReceipt returns an OnEvent hook that compares the child's own report of what it
// received against what was declared, and refuses before any model output.
//
// This is the difference between a bound that is asserted and one that is verified. No
// amount of care constructing the argument vector can prove the process ended up with
// what the vector asked for — a flag an older executable does not recognise is ignored
// rather than refused, so a bound expressed as a flag fails open. The child says what it
// actually has, and this is where that is read.
//
// Wider is refused; narrower is not. A process that ended up with fewer tools than the
// playbook asked for cannot exceed the declaration, and refusing that would turn a
// harmless difference into an outage.
func CheckReceipt(decl Declaration) func(Event) error {
	declaredTools := set(decl.Tools)
	declaredServers := set(decl.MCPServers)

	return func(event Event) error {
		if event.Type != "system" || event.Subtype != "init" {
			return nil
		}

		var extraTools []string
		for _, tool := range event.Tools {
			if !declaredTools[tool] {
				extraTools = append(extraTools, tool)
			}
		}
		var extraServers []string
		for _, server := range event.MCPServers {
			if !declaredServers[server.Name] {
				extraServers = append(extraServers, server.Name)
			}
		}
		if len(extraTools) == 0 && len(extraServers) == 0 {
			return nil
		}

		var said []string
		if len(extraTools) > 0 {
			sort.Strings(extraTools)
			said = append(said, fmt.Sprintf("tools not declared: %s", strings.Join(extraTools, ", ")))
		}
		if len(extraServers) > 0 {
			sort.Strings(extraServers)
			said = append(said, fmt.Sprintf("MCP servers not declared: %s", strings.Join(extraServers, ", ")))
		}
		return fmt.Errorf("%w: %s", ErrReceiptMismatch, strings.Join(said, "; "))
	}
}

func set(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
