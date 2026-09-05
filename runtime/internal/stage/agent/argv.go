package agent

import (
	"encoding/json"
	"errors"
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
	flagVerbose        = "--verbose"
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

// ErrSeparatorInEntry is returned for a declared entry holding a comma.
//
// The entries are not joined any more — each one is its own argument, which is what these
// flags accept and what removes the smuggling at its source. The comma is still refused
// because the flag's own parser takes "a comma or space separated list", so one argument
// holding "Read,Bash" can still become two inside the process, and a per-entry validator
// would have passed it.
//
// A space is NOT refused: `Bash(git *)` is the CLI's own documented example of a single
// entry, and refusing it would refuse a legitimate declaration. Passing each entry as its
// own argument is the strongest form available; whether the process then splits a single
// argument on its internal spaces was not established, and is not claimed here.
var ErrSeparatorInEntry = errors.New("a declared entry contains a comma, which the flag's parser may split")

// BuildArgs constructs the argument vector.
//
// The bounding flags — the tool set, the strict MCP configuration and the setting
// sources — are never omitted, and that is the whole design. A playbook
// declaring no MCP servers still gets `--strict-mcp-config` against an empty
// configuration, because omitting the flag inherits the machine's; a playbook declaring
// no tools still gets `--tools ""`, because omitting it leaves the built-in set. An
// absent flag is not a neutral default here, it is the machine's own configuration.
//
// The prompt is not in the vector. It goes on stdin, because interpolation can put a
// configured value into it and the constitution does not allow a secret on a command
// line. The MCP configuration is a file for the same reason: a server entry can carry a
// token.
func BuildArgs(decl Declaration, mcpConfigPath string) ([]string, error) {
	for _, group := range [][]string{decl.Tools, decl.Allow} {
		for _, entry := range group {
			if strings.Contains(entry, ",") {
				return nil, fmt.Errorf("%w: %q", ErrSeparatorInEntry, entry)
			}
		}
	}

	args := []string{
		flagPrint,
		// Required: the executable refuses --output-format=stream-json under --print
		// without it. Found by running it, after a first probe stopped on the missing
		// prompt before reaching this validation.
		flagVerbose,
		flagOutputFormat, "stream-json",
		// No user, project or local settings. `--restricted` says it ignores them too,
		// but only while it is on, and this holds when a playbook has turned it off.
		flagSettingSources, "",
		flagStrictMCP,
		flagMCPConfig, mcpConfigPath,
	}

	// One argument per entry rather than one joined argument: both flags are variadic,
	// and a list that is never joined cannot be split back apart into something nobody
	// declared. "" disables the built-in set, so a playbook naming nothing gets nothing —
	// and that empty argument has to be there, because an absent --tools leaves the
	// built-in set intact.
	args = append(args, flagTools)
	if len(decl.Tools) == 0 {
		args = append(args, "")
	} else {
		args = append(args, decl.Tools...)
	}
	if decl.Restricted {
		args = append(args, flagRestricted)
	}
	if len(decl.Allow) > 0 {
		args = append(args, flagAllowedTools)
		args = append(args, decl.Allow...)
	}
	if decl.Model != "" {
		args = append(args, flagModel, decl.Model)
	}
	if decl.OutputSchema != nil {
		schema, err := json.Marshal(decl.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("the declared output schema cannot be encoded: %w", err)
		}
		args = append(args, flagJSONSchema, string(schema))
	}
	return args, nil
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
