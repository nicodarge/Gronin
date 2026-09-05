package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Declaration is the bound a playbook declared, resolved and ready to apply.
type Declaration struct {
	Model        string
	Restricted   bool
	Tools        []string
	MCPServers   []string
	Allow        []string
	OutputSchema any
}

// The flags below were read off the shipped executable (2.1.261) rather than inferred:
// `--tools` names the built-in set and takes "" to disable all of it, `--allowedTools`
// takes a comma or space separated list, `--mcp-config` takes files or JSON strings,
// and `--setting-sources` takes a comma-separated list of sources to load. A flag an
// older executable does not recognise is IGNORED rather than refused, which is why the
// version floor exists: a bound expressed as a flag fails open.
const (
	flagPrint          = "--print"
	flagOutputFormat   = "--output-format"
	flagRestricted     = "--restricted"
	flagTools          = "--tools"
	flagAllowedTools   = "--allowedTools"
	flagMCPConfig      = "--mcp-config"
	flagStrictMCP      = "--strict-mcp-config"
	flagSettingSources = "--setting-sources"
	flagJSONSchema     = "--json-schema"
	flagModel          = "--model"
)

// BuildArgs constructs the argument vector.
//
// None of the bounding flags is ever omitted, and that is the whole design. A playbook
// declaring no MCP servers still gets `--strict-mcp-config` against an empty
// configuration, because omitting the flag inherits the machine's; a playbook declaring
// no tools still gets `--tools ""`, because omitting it leaves the built-in set. An
// absent flag is not a neutral default here, it is the machine's own configuration.
//
// The prompt is not in the vector. It goes on stdin, because interpolation can put a
// configured value into it and the constitution does not allow a secret on a command
// line. The MCP configuration is a file for the same reason: a server entry can carry a
// token.
func BuildArgs(decl Declaration, mcpConfigPath string) []string {
	args := []string{
		flagPrint,
		flagOutputFormat, "stream-json",
		// No user, project or local settings. `--restricted` says it ignores them too,
		// but only while it is on, and this holds when a playbook has turned it off.
		flagSettingSources, "",
		flagStrictMCP,
		flagMCPConfig, mcpConfigPath,
		// "" disables the built-in set. A playbook naming nothing gets nothing.
		flagTools, strings.Join(decl.Tools, ","),
	}
	if decl.Restricted {
		args = append(args, flagRestricted)
	}
	if len(decl.Allow) > 0 {
		args = append(args, flagAllowedTools, strings.Join(decl.Allow, ","))
	}
	if decl.Model != "" {
		args = append(args, flagModel, decl.Model)
	}
	if decl.OutputSchema != nil {
		if schema, err := json.Marshal(decl.OutputSchema); err == nil {
			args = append(args, flagJSONSchema, string(schema))
		}
	}
	return args
}

// WriteMCPConfig writes the strict configuration this run is bounded by and returns its
// path. An empty server list is written as an empty configuration rather than skipped:
// the file existing is what makes `--strict-mcp-config` mean "these and no others".
func WriteMCPConfig(dir string, servers map[string]any) (string, error) {
	if servers == nil {
		servers = map[string]any{}
	}
	document, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		return "", fmt.Errorf("encoding the MCP configuration: %w", err)
	}
	path := filepath.Join(dir, "mcp.json")
	// 0600: a server entry can carry a credential, and this file lives in a working
	// directory the gather steps also wrote to.
	if err := os.WriteFile(path, document, 0o600); err != nil {
		return "", fmt.Errorf("writing the MCP configuration: %w", err)
	}
	return path, nil
}
