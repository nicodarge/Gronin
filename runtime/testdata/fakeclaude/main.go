// Command fakeclaude stands in for the agent process in tests.
//
// It speaks the part of the command-line contract the runtime depends on: it accepts the
// containment flags, and it emits `stream-json` events shaped like the ones Phase 0
// observed — a `system`/`init` first, carrying the tool set the process actually ended
// up with, and a `result` last, carrying cost, usage and the actions the bounds refused.
//
// The point of the receipt is that it reports what this process received rather than
// what the caller meant to send, so the runtime can assert one against the other. The
// mismatch mode below is what proves that assertion can fail: it reports a WIDER tool set
// than it was given, which is the shape of the failure that matters — a bound that was
// declared and not applied.
//
// Every agent-stage test uses this. None of them spends a token.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// mode is read from the environment rather than a flag: the runtime builds this
// process's argument vector from a playbook, and a test must not have to reach into
// that to choose a behaviour.
const modeVar = "FAKECLAUDE_MODE"

const (
	modeSuccess   = "success"    // the default: init, one assistant message, a result
	modeTimeout   = "timeout"    // emits init and then never terminates
	modeMalformed = "malformed"  // emits a line that is not JSON
	modeMismatch  = "mismatch"   // reports a tool set wider than it was given
	modeExitError = "exit-error" // emits a failing result and exits non-zero
	modeUnknown   = "unknown"    // emits event types and fields the runtime has never seen
)

// Version is what this reports as the CLI version, high enough to clear a floor.
const Version = "2.1.261"

func main() {
	flags := parseFlags(os.Args[1:])

	if len(flags["version"]) > 0 || hasBare(os.Args[1:], "--version") {
		fmt.Println(Version + " (fakeclaude)")
		return
	}

	// --tools and --allowedTools are variadic on the real executable, so the receipt has
	// to be built from every argument the flag consumed. A stub that read only the first
	// one reported a narrower tool set than the process was given, which is the wrong
	// direction for a receipt to be wrong in.
	tools := entries(flags["tools"])
	servers := serversFrom(first(flags["mcp-config"]))

	mode := os.Getenv(modeVar)
	if mode == "" {
		mode = modeSuccess
	}

	if mode == modeMismatch {
		// Wider than it was given. A bound that was declared and not applied is the
		// failure the receipt check exists for.
		tools = append(tools, "Bash")
	}

	emit(map[string]any{
		"type": "system", "subtype": "init",
		"session_id":          "fake-session-0001",
		"model":               first(flags["model"]),
		"permissionMode":      first(flags["permission-mode"]),
		"apiKeySource":        apiKeySource(),
		"mcp_servers":         servers,
		"claude_code_version": Version,
		"tools":               tools,
	})

	switch mode {
	case modeTimeout:
		// A sleep rather than a blocked channel: with every goroutine parked on a sync
		// primitive the Go runtime calls it a deadlock and exits 2, so `select {}` here
		// terminated on its own — the opposite of what this mode is for.
		time.Sleep(24 * time.Hour)
	case modeMalformed:
		fmt.Println("this line is not JSON, and the decoder is expected to survive it")
	case modeUnknown:
		// A CLI update must degrade rather than break: unknown types and unknown fields.
		emit(map[string]any{"type": "something_new", "payload": map[string]any{"a": 1}})
		emit(map[string]any{"type": "assistant", "message": "hello", "field_from_the_future": true})
	}

	// Shaped like the real thing: content is a list of blocks, and a tool call is a
	// tool_use block. The record reads them from here, so a stub emitting a bare string
	// would make the extractor look like it worked.
	emit(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "reading the gathered facts"},
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_0001",
					"name":  "Read",
					"input": map[string]any{"file_path": "facts.json"},
				},
			},
		},
	})
	emit(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_0001",
					"content":     "{\"drift\": 0}",
				},
			},
		},
	})

	result := map[string]any{
		"type": "result", "subtype": "success",
		"total_cost_usd":  0.0123,
		"usage":           map[string]any{"input_tokens": 100, "output_tokens": 25},
		"num_turns":       1,
		"duration_ms":     12,
		"duration_api_ms": 9,
		"is_error":        false,
		"stop_reason":     "end_turn",
		"terminal_reason": "done",
		"result":          resultPayload(),
		"permission_denials": []any{
			map[string]any{"tool": "Bash", "reason": "not in the declared tool set"},
		},
	}
	if mode == modeExitError {
		result["subtype"] = "error"
		result["is_error"] = true
		result["stop_reason"] = "error"
		result["result"] = "the agent failed"
	}
	emit(result)

	if mode == modeExitError {
		os.Exit(1)
	}
}

// resultPayload is what the agent "answered". A test that needs a particular report
// hands it over rather than matching whatever the stub invented.
func resultPayload() any {
	if raw := os.Getenv("FAKECLAUDE_RESULT"); raw != "" {
		var decoded any
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			return decoded
		}
		return raw
	}
	return map[string]any{"findings": []any{}}
}

// apiKeySource reports where a credential came from, the way the real process does. The
// runtime reads this rather than asserting a source of its own.
func apiKeySource() string {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "ANTHROPIC_API_KEY"
	}
	if source := os.Getenv("FAKECLAUDE_KEY_SOURCE"); source != "" {
		return source
	}
	return "none"
}

func emit(event map[string]any) {
	line, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(line))
	// A real process interleaves; a stub that emits everything in one scheduler slice
	// makes a decoder look more robust than it is.
	time.Sleep(time.Millisecond)
}

// parseFlags reads --name=value and --name value... into a map of lists, because the
// flags that carry the bounds are variadic. It is deliberately forgiving otherwise: this
// stands in for a CLI whose flag set the runtime does not control, and a stub that
// refused an unfamiliar flag would fail tests for the wrong reason.
func parseFlags(args []string) map[string][]string {
	flags := map[string][]string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name := strings.TrimPrefix(arg, "--")
		if name, value, found := strings.Cut(name, "="); found {
			flags[name] = append(flags[name], value)
			continue
		}
		consumed := false
		for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			flags[name] = append(flags[name], args[i+1])
			i++
			consumed = true
		}
		if !consumed {
			flags[name] = append(flags[name], "true")
		}
	}
	return flags
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// entries is the tool set the process ended up with. A single empty argument is how the
// built-in set is disabled, and it means no tools rather than one tool with no name.
func entries(values []string) []string {
	out := []string{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

func hasBare(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// serversFrom reports the MCP servers this process was actually configured with, read
// from the file it was pointed at. An empty configuration is the normal case and is not
// the same as no flag at all.
func serversFrom(path string) []any {
	if path == "" || path == "true" {
		return []any{}
	}
	data, err := os.ReadFile(path) //nolint:gosec // a path this process was handed by its caller
	if err != nil {
		return []any{}
	}
	var config struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return []any{}
	}
	servers := make([]any, 0, len(config.MCPServers))
	for name := range config.MCPServers {
		servers = append(servers, map[string]any{"name": name, "status": "connected"})
	}
	return servers
}
