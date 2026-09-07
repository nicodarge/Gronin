// Package mcpcatalog holds the deployment's own MCP server catalogue: what a playbook may
// name, and how to reach it.
//
// It lives beside the deployment configuration and never inside a playbook or a
// repository: a real entry carries a hostname and often a credential reference. It never
// holds a resolved secret either — an entry's env and header values are references into
// the deployment configuration (internal/config), resolved at run time and not before.
package mcpcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one MCP server this deployment provides, exactly one of two transports:
//
//   - stdio: Command, with optional Args and Env.
//   - http: URL, with optional Headers.
//
// An entry naming both transports, or neither, is refused when the catalogue is loaded.
type Entry struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (e Entry) isStdio() bool { return e.Command != "" }
func (e Entry) isHTTP() bool  { return e.URL != "" }

func (e Entry) validate(name string) error {
	switch {
	case e.isStdio() && e.isHTTP():
		return fmt.Errorf(
			"mcp server %q names both a command and a url; accepted: exactly one transport", name)
	case !e.isStdio() && !e.isHTTP():
		return fmt.Errorf(
			"mcp server %q names neither a command nor a url; accepted: a command (stdio) or a url (http)",
			name)
	}
	return nil
}

// Catalog is what this deployment provides.
type Catalog struct {
	entries map[string]Entry
}

const catalogFile = "mcp_servers.json"

// Load reads the catalogue from the deployment's state directory, the way
// internal/config.Load reads config.json beside it. Absent is not an error: it is a
// deployment that provides no MCP server, which is today's behaviour.
func Load(stateDir string) (*Catalog, error) {
	// The path is this deployment's own state directory and a fixed name; there is no
	// caller-supplied component in it.
	data, err := os.ReadFile(filepath.Join(stateDir, catalogFile)) //nolint:gosec
	if errors.Is(err, os.ErrNotExist) {
		return &Catalog{entries: map[string]Entry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the MCP server catalogue: %w", err)
	}

	entries := map[string]Entry{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing the MCP server catalogue: %w", err)
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)

	var problems []error
	for _, name := range names {
		if err := entries[name].validate(name); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return &Catalog{entries: entries}, nil
}

// Names is every server this deployment provides, ordered. This is what the load gate
// (playbook.Deployment.MCPServers) checks a playbook's agent.mcp entries against, so a
// server the catalogue does not hold is refused before a run exists.
func (c *Catalog) Names() []string {
	names := make([]string, 0, len(c.entries))
	for name := range c.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolve builds the mcpServers entry a named server contributes to the strict
// configuration a run is bounded by, interpolating its env or header values against the
// deployment configuration. Only those are resolved: the command, its arguments and the
// url travel as written, because the catalogue's own contract is that a credential is
// never one of them.
//
// interpolate is injected rather than an internal/config.Config taken directly, matching
// how internal/sink.BuildOptions stays decoupled from that package.
func (c *Catalog) Resolve(name string, interpolate func(string) (string, error)) (map[string]any, error) {
	entry, ok := c.entries[name]
	if !ok {
		return nil, fmt.Errorf("mcp server %q is not in the catalogue", name)
	}

	if entry.isStdio() {
		out := map[string]any{"command": entry.Command}
		if len(entry.Args) > 0 {
			out["args"] = entry.Args
		}
		env, err := resolveAll(name, "env", entry.Env, interpolate)
		if err != nil {
			return nil, err
		}
		if len(env) > 0 {
			out["env"] = env
		}
		return out, nil
	}

	out := map[string]any{"type": "http", "url": entry.URL}
	headers, err := resolveAll(name, "header", entry.Headers, interpolate)
	if err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		out["headers"] = headers
	}
	return out, nil
}

// resolveAll interpolates every value in a declared env or header map. A reference that
// resolves to nothing is refused naming both the server and the key, rather than the bare
// message internal/config.Interpolate returns on its own.
func resolveAll(
	server, kind string, values map[string]string, interpolate func(string) (string, error),
) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make(map[string]string, len(values))
	for _, key := range keys {
		resolved, err := interpolate(values[key])
		if err != nil {
			return nil, fmt.Errorf("mcp server %q: %s %q: %w", server, kind, key, err)
		}
		out[key] = resolved
	}
	return out, nil
}

// Summary is what an operator-facing listing (`gronin mcp list`) prints for one server:
// its transport, where it reaches, and which env or header keys it sets — never a
// resolved value. This never calls Resolve, so it cannot print what that produces.
func (c *Catalog) Summary(name string) string {
	entry, ok := c.entries[name]
	if !ok {
		return ""
	}
	if entry.isStdio() {
		args := strings.Join(entry.Args, " ")
		if args != "" {
			args = " " + args
		}
		return fmt.Sprintf("stdio  %s%s%s", entry.Command, args, keysOf("env", entry.Env))
	}
	return fmt.Sprintf("http   %s%s", entry.URL, keysOf("headers", entry.Headers))
}

func keysOf(label string, values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return fmt.Sprintf(" (%s: %s)", label, strings.Join(keys, ", "))
}

// CheckReferences refuses a catalogue whose env or header values this deployment cannot
// resolve, using the same resolver a run uses rather than a second reading of the
// reference syntax — a copy would drift, and the run-time path is what an operator
// eventually meets.
//
// Without it this is the one class of ${config.x} that surfaces at the first run rather
// than at load: the gate already checks a playbook's own references against the same key
// set, and a catalogue reference is the same kind of reference.
func (c *Catalog) CheckReferences(interpolate func(string) (string, error)) error {
	var problems []error
	for _, name := range c.Names() {
		entry := c.entries[name]
		if _, err := resolveAll(name, "env", entry.Env, interpolate); err != nil {
			problems = append(problems, err)
		}
		if _, err := resolveAll(name, "header", entry.Headers, interpolate); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}
