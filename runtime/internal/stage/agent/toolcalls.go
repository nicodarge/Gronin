package agent

import (
	"encoding/json"
	"time"
)

// ToolCall is one tool invocation the transcript reported, paired with its result.
//
// FR-026 requires every tool call with its input and output, and the stream carries them
// as content blocks rather than as events of their own: a `tool_use` block inside an
// assistant message, and a `tool_result` block inside the user message that answers it.
// They are matched by the identifier the first one carries.
type ToolCall struct {
	ID        string
	Name      string
	Input     json.RawMessage
	Output    json.RawMessage
	StartedAt time.Time
	Outcome   string
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type message struct {
	Content []contentBlock `json:"content"`
}

// collector accumulates tool calls as events arrive, so a run that is killed still has
// the calls it made before it stopped.
type collector struct {
	calls []ToolCall
	byID  map[string]int
}

func newCollector() *collector { return &collector{byID: map[string]int{}} }

func (c *collector) observe(raw json.RawMessage, at time.Time) {
	if len(raw) == 0 {
		return
	}
	var decoded message
	if err := json.Unmarshal(raw, &decoded); err != nil {
		// A message whose content is a plain string, or a shape this version does not
		// know. Not an error: the decoder degrades rather than breaks.
		return
	}

	for _, block := range decoded.Content {
		switch block.Type {
		case "tool_use":
			c.byID[block.ID] = len(c.calls)
			c.calls = append(c.calls, ToolCall{
				ID: block.ID, Name: block.Name, Input: block.Input,
				StartedAt: at, Outcome: "ok",
			})
		case "tool_result":
			at, known := c.byID[block.ToolUseID]
			if !known {
				continue
			}
			c.calls[at].Output = block.Content
			if block.IsError {
				c.calls[at].Outcome = "error"
			}
		}
	}
}
