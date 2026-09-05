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
	// The allowlist counts, but only the part of it that can appear in a receipt.
	//
	// Measured against the shipped executable rather than assumed: run with
	// `--tools "" --allowedTools "Read(./**)"`, the receipt reports `tools: []`. The
	// allowlist governs permission, not existence, so it does not add to the reported
	// set — and folding the whole of it in, which is what the first version of this fix
	// did, WIDENED what the check accepts and would have hidden a mismatch rather than
	// caught one.
	//
	// What can legitimately appear is an individually named MCP tool, because a
	// connected server's tools do show up. Those are folded in; a scoped file form like
	// `Read(./**)` is not a tool name and is not.
	declaredTools := set(decl.Tools, mcpToolsIn(decl.Allow))
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

func set(groups ...[]string) map[string]bool {
	out := map[string]bool{}
	for _, group := range groups {
		for _, value := range group {
			out[value] = true
		}
	}
	return out
}

// mcpToolsIn keeps the allowlist entries that name a tool a receipt could report: a
// fully-qualified MCP tool. Everything else in an allowlist is a scoped file form, which
// is a permission on a tool rather than a tool.
//
// This is deliberately looser than the load gate's own reading of the same shape
// (playbook.mcpShape, which requires a well-formed tool name after the server). It can
// afford to be: the gate refuses anything else before a run exists, so a shape that gets
// here has already passed it. If that stops being true — if the gate's toolName ever
// widens — this widens with it rather than against it, which is the safe direction for
// the two to drift.
func mcpToolsIn(allow []string) []string {
	var tools []string
	for _, entry := range allow {
		if strings.HasPrefix(entry, "mcp__") && strings.Count(entry, "__") >= 2 {
			tools = append(tools, entry)
		}
	}
	return tools
}
