// Package playbook parses a playbook into its typed shape and validates the semantics a
// schema cannot express. The refusal rules live here.
package playbook

import (
	"fmt"
	"time"
)

// Playbook is the declarative unit an operator writes and shares. It holds no hostname,
// no address, no credential and no identifier belonging to one deployment: those arrive
// through the deployment's configuration and through the trigger payload.
type Playbook struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Trigger     Trigger     `yaml:"trigger"`
	Gather      []Step      `yaml:"gather"`
	Agent       Agent       `yaml:"agent"`
	Sinks       []Sink      `yaml:"sinks"`
	Guard       *Guard      `yaml:"guard"`
	Retrieve    []Retrieval `yaml:"retrieve"`

	// Path is where this was read from. Not part of the document — and `json:"-"` is
	// what makes that true of the JSON copy the record keeps, which a replay compares
	// against. Without it, moving the playbook directory, or replaying with a
	// differently-spelled --playbooks flag than the cron job used, refused a playbook
	// whose content had not changed at all.
	Path string `yaml:"-" json:"-"`
}

// Trigger is how a playbook comes to run.
type Trigger struct {
	Type     string `yaml:"type"`
	Schedule string `yaml:"schedule"`
	// Source is the deployment's configured source this webhook trigger is bound to
	// (FR-310). Empty for a cron or manual trigger.
	Source string `yaml:"source"`
	// Values is every payload value a webhook trigger declares (FR-322). Nothing in a
	// delivery beyond these reaches the run (FR-323).
	Values map[string]TriggerValue `yaml:"values"`
}

// TriggerValue is one payload value a webhook trigger declares: where it is in the body,
// the pattern it must wholly match, and its maximum length in Unicode code points.
type TriggerValue struct {
	At        string `yaml:"at"`
	Pattern   string `yaml:"pattern"`
	MaxLength int    `yaml:"max_length"`
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

// Guard is what a playbook declares about whether a trigger becomes a run (FR-119). No
// backend, credential or duration of the claim set appears here: those belong to the
// deployment, and a playbook declaring a limit stays portable to one that coordinates
// differently.
type Guard struct {
	Rate *Rate `yaml:"rate"`
	// Wait is how long a trigger refused because the playbook is running may wait for it
	// (FR-112). Empty when not declared, which is not the same as "0s".
	Wait string `yaml:"wait"`
}

// DefaultWait is how long a trigger waits when its playbook does not say: the agent stage's
// own default timeout, so that a waiting trigger does not expire merely because the run
// ahead of it used its declared budget (specs/002-guard/research.md §3).
const DefaultWait = 30 * time.Minute

// WaitFor is how long a trigger refused because this playbook is running may wait for it
// (FR-112).
func (p *Playbook) WaitFor() (time.Duration, error) {
	if p.Guard == nil || p.Guard.Wait == "" {
		return DefaultWait, nil
	}
	wait, err := time.ParseDuration(p.Guard.Wait)
	if err != nil {
		return 0, fmt.Errorf("guard.wait %q is not a duration: %w", p.Guard.Wait, err)
	}
	return wait, nil
}

// Rate is a limit of runs per window, keyed on the playbook name (FR-114).
type Rate struct {
	Runs int    `yaml:"runs"`
	Per  string `yaml:"per"`
}

// Retrieval is one search the runtime performs before the agent runs, written into the
// working directory where the agent reads it as it reads gathered input.
//
// No mode, endpoint, model or credential appears here. Those belong to the deployment's
// collection, so a playbook retrieving from `runbooks` runs against a deployment whose
// `runbooks` is lexical and one whose `runbooks` is semantic alike.
type Retrieval struct {
	Collection string `yaml:"collection"`
	Query      string `yaml:"query"`
	// QueryFrom is the `as` of a gather step whose output is the query. Exactly one of
	// Query and QueryFrom. A gathered input is a file the run already wrote, and reading
	// it adds no source to interpolation, which the constitution bounds to two.
	QueryFrom  string `yaml:"query_from"`
	As         string `yaml:"as"`
	MaxResults int    `yaml:"max_results"`
	MaxBytes   int    `yaml:"max_bytes"`
}

// What a retrieval returns when the playbook says nothing, and the most it may ask for.
// The ceilings are the runtime's; the values below them are the playbook's.
const (
	DefaultMaxResults = 10
	MaxResultsCeiling = 50
	DefaultMaxBytes   = 16384
	MaxBytesCeiling   = 65536
)

// ResultCount is how many results this retrieval returns.
func (r Retrieval) ResultCount() int {
	if r.MaxResults == 0 {
		return DefaultMaxResults
	}
	return r.MaxResults
}

// ByteBound is the largest results file this retrieval may write, every byte counted —
// the lines naming each result's source included.
func (r Retrieval) ByteBound() int {
	if r.MaxBytes == 0 {
		return DefaultMaxBytes
	}
	return r.MaxBytes
}

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
//
// The configuration comes back as a plain map. YAML decodes a nested mapping into this
// named type rather than into map[string]any, so a caller asserting the underlying type
// gets nothing — which read as a sink with no configuration at all.
func (s Sink) Type() (string, map[string]any, bool) {
	if len(s) != 1 {
		return "", nil, false
	}
	for name, value := range s {
		switch typed := value.(type) {
		case Sink:
			return name, map[string]any(typed), true
		case map[string]any:
			return name, typed, true
		case nil:
			return name, map[string]any{}, true
		default:
			// A scalar under a sink key. Returning an empty configuration here would
			// make it read as a sink that configured nothing, and the refusal would name
			// the missing field rather than the shape that is wrong.
			return name, nil, false
		}
	}
	return "", nil, false
}
