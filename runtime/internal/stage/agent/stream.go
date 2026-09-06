package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"time"
)

// Event is the part of the stream the runtime depends on.
//
// Unknown event types and unknown fields are ignored rather than refused: Phase 0
// observed one successful run on one version, and a decoder that fails on an event it
// has not seen turns a CLI update into an outage. The version floor is what protects the
// fields below; everything else may come and go.
type Event struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// system/init — the receipt. `tools` and `mcp_servers` are what the child actually
	// ended up with, which is what makes the bound verifiable rather than asserted.
	SessionID         string      `json:"session_id"`
	Model             string      `json:"model"`
	PermissionMode    string      `json:"permissionMode"`
	APIKeySource      string      `json:"apiKeySource"`
	MCPServers        []MCPServer `json:"mcp_servers"`
	ClaudeCodeVersion string      `json:"claude_code_version"`
	Tools             []string    `json:"tools"`

	// result — the terminal event.
	TotalCostUSD   float64         `json:"total_cost_usd"`
	Usage          Usage           `json:"usage"`
	NumTurns       int             `json:"num_turns"`
	DurationMS     int64           `json:"duration_ms"`
	IsError        bool            `json:"is_error"`
	StopReason     string          `json:"stop_reason"`
	TerminalReason string          `json:"terminal_reason"`
	Result         json.RawMessage `json:"result"`
	// StructuredOutput is the answer as an object, present when the run was given
	// --json-schema. `result` carries the same answer encoded as a JSON string, so
	// validating that against the declared schema reports "got string, want object" —
	// which is what every real run did before this field was read.
	StructuredOutput json.RawMessage `json:"structured_output"`

	// assistant and user events carry the tool calls, as content blocks rather than as
	// events of their own.
	Message           json.RawMessage    `json:"message"`
	PermissionDenials []PermissionDenial `json:"permission_denials"`
}

// MCPServer is one server the child reported it has.
type MCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Usage is the token accounting from the terminal event.
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// Total is every token the run was charged for.
func (u Usage) Total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

// PermissionDenial is one action the bounds refused. Every one of them is recorded: a
// run that succeeds while repeatedly reaching for something it cannot have is telling
// you its declared tool set is wrong, or that its prompt is steering somewhere else.
//
// The field names are the executable's, read off a real run's terminal event rather than
// guessed: it emits `tool_name` and `tool_input`, and there is no `reason` at all. Named
// `tool` and `reason` here, both decoded empty and `gronin show` printed a bare
// "refused   : " — the one section that only ever appears when something went wrong was
// the one saying nothing.
type PermissionDenial struct {
	Tool string `json:"tool_name"`
	// Input is what the agent asked for, which is the whole of what makes a refusal
	// actionable: the pattern or path it reached for says whether the playbook's tool set
	// is wrong or its prompt is steering somewhere it should not.
	Input json.RawMessage `json:"tool_input"`
}

// RefusedByCallback is what Decode returns when onEvent refused the run, so a caller can
// tell a bound being enforced from the stream simply failing.
type RefusedByCallback struct{ inner error }

func (e *RefusedByCallback) Error() string { return e.inner.Error() }

// Unwrap returns the callback's own error.
func (e *RefusedByCallback) Unwrap() error { return e.inner }

// Stream is what one decode of the child's output produced.
type Stream struct {
	Init      *Event
	Result    *Event
	ToolCalls []ToolCall
	Events    int
	Undecoded int // lines that were not JSON; counted, not fatal
	Raw       []byte
}

// maxLine bounds one event. A single event larger than this is not an event the runtime
// can use, and reading it would let the child decide how much memory this process takes.
const maxLine = 8 << 20

// Decode reads the child's stream, keeping the raw bytes for the record and reporting
// the two events the runtime acts on.
//
// onEvent is called for every decoded event, so a caller can react to the receipt before
// the run produces output — which is what the receipt check needs.
func Decode(from io.Reader, onEvent func(Event) error) (*Stream, error) {
	stream := &Stream{}
	calls := newCollector()
	var raw []byte

	scanner := bufio.NewScanner(from)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLine)

	for scanner.Scan() {
		line := scanner.Bytes()
		raw = append(raw, line...)
		raw = append(raw, '\n')

		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			stream.Undecoded++
			continue
		}
		stream.Events++

		if event.Type == "assistant" || event.Type == "user" {
			calls.observe(event.Message, time.Now().UTC())
		}

		switch {
		case event.Type == "system" && event.Subtype == "init" && stream.Init == nil:
			held := event
			stream.Init = &held
		case event.Type == "result":
			held := event
			stream.Result = &held
		}

		if onEvent != nil {
			if err := onEvent(event); err != nil {
				stream.Raw = raw
				stream.ToolCalls = calls.calls
				return stream, &RefusedByCallback{inner: err}
			}
		}
	}
	stream.Raw = raw
	stream.ToolCalls = calls.calls
	return stream, scanner.Err()
}

// Elapsed is the duration the terminal event reported, when it reported one.
func (s *Stream) Elapsed() time.Duration {
	if s.Result == nil {
		return 0
	}
	return time.Duration(s.Result.DurationMS) * time.Millisecond
}
