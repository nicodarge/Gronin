// Package playbook parses a playbook into its typed shape and validates the semantics a
// schema cannot express. The refusal rules live here.
package playbook

import "time"

// Playbook is the declarative unit an operator writes and shares. It holds no hostname,
// no address, no credential and no identifier belonging to one deployment: those arrive
// through the deployment's configuration and through the trigger payload.
type Playbook struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Trigger     Trigger  `yaml:"trigger"`
	Gather      []Step   `yaml:"gather"`
	Agent       Agent    `yaml:"agent"`
	Sinks       []Sink   `yaml:"sinks"`
	Guard       *Unknown `yaml:"guard"`
	Retrieve    *Unknown `yaml:"retrieve"`

	// Path is where this was read from. Not part of the document; the refusal output
	// names the file, and a prompt path resolves relative to it.
	Path string `yaml:"-"`
}

// Trigger is how a playbook comes to run. Webhooks are a later feature.
type Trigger struct {
	Type     string `yaml:"type"`
	Schedule string `yaml:"schedule"`
}

// Step is one gather command and the name it writes into the working directory.
type Step struct {
	Run string `yaml:"run"`
	As  string `yaml:"as"`
}

// Agent is where the bounds live.
type Agent struct {
	Model        string         `yaml:"model"`
	PromptFile   string         `yaml:"prompt_file"`
	Restricted   *bool          `yaml:"restricted"`
	Tools        []string       `yaml:"tools"`
	MCP          []string       `yaml:"mcp"`
	Allow        []string       `yaml:"allow"`
	OutputSchema any            `yaml:"output_schema"`
	Timeout      string         `yaml:"timeout"`
	Extra        map[string]any `yaml:",inline"`
}

// Sink is one destination. Exactly one key names the type; its value configures it.
type Sink map[string]any

// Unknown marks a block the runtime does not apply. Its presence is refused rather than
// ignored: a declared bound nothing enforces reads as enforced in review, which is worse
// than an absent one.
type Unknown struct{}

// DefaultTimeout is the agent stage timeout when a playbook declares none.
const DefaultTimeout = 30 * time.Minute

// IsRestricted reports whether the coarsest bound is on. It defaults to true, so a
// playbook that says nothing gets it — turning it off has to be a decision someone wrote
// down, not an omission.
func (a Agent) IsRestricted() bool {
	return a.Restricted == nil || *a.Restricted
}

// StageTimeout is the declared timeout, or the default.
func (a Agent) StageTimeout() (time.Duration, error) {
	if a.Timeout == "" {
		return DefaultTimeout, nil
	}
	return time.ParseDuration(a.Timeout)
}

// Type returns the sink's type and its configuration. A sink carries exactly one key;
// the shape layer enforces that, and this reports what it found either way.
func (s Sink) Type() (string, any, bool) {
	if len(s) != 1 {
		return "", nil, false
	}
	for name, value := range s {
		return name, value, true
	}
	return "", nil, false
}
