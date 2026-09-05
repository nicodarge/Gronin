package sink

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Declaration is one sink as a playbook declared it: the type it names and the
// configuration under it. The runtime converts a playbook's sink block into this, so
// this package does not depend on the playbook's shape.
type Declaration struct {
	Type   string
	Config map[string]any
}

// BuildOptions are what a deployment supplies around a declaration.
type BuildOptions struct {
	// Interpolate resolves ${config.x} and ${trigger.x} in a declared value. A playbook
	// holds the reference; the deployment holds the value.
	Interpolate func(string) (string, error)
	// Client, when set, is the HTTP client the messaging sinks use. Tests supply one.
	Client *http.Client
}

// Build turns declarations into sinks, refusing a type this deployment does not
// implement (FR-038) and a creating sink with no cap (FR-006, applied by CheckCap).
//
// Every refusal is collected rather than the first returned: fixing them one round trip
// at a time is how a gate gets switched off.
func Build(declared []Declaration, opts BuildOptions) ([]Sink, []error) {
	var (
		sinks    []Sink
		problems []error
	)

	for at, decl := range declared {
		if decl.Type == "" {
			problems = append(problems, fmt.Errorf(
				"sinks[%d]: names no type; accepted: one key naming the sink, e.g. %s",
				at, Types()[0]))
			continue
		}
		if decl.Config == nil {
			problems = append(problems, fmt.Errorf(
				"sinks[%d].%s: its value is not a mapping; accepted: the settings for "+
					"that sink, e.g. webhook: ${config.%s_webhook}",
				at, decl.Type, decl.Type))
			continue
		}
		built, err := buildOne(decl, opts)
		if err != nil {
			problems = append(problems, fmt.Errorf("sinks[%d].%s: %w", at, decl.Type, err))
			continue
		}
		if err := CheckCap(built); err != nil {
			problems = append(problems, fmt.Errorf("sinks[%d].%s: %w", at, decl.Type, err))
			continue
		}
		sinks = append(sinks, built)
	}
	return sinks, problems
}

func buildOne(decl Declaration, opts BuildOptions) (Sink, error) {
	switch decl.Type {
	case "discord":
		url, err := webhookURL(decl, opts)
		if err != nil {
			return nil, err
		}
		return NewDiscord(url, opts.Client), nil
	case "slack":
		url, err := webhookURL(decl, opts)
		if err != nil {
			return nil, err
		}
		return NewSlack(url, opts.Client), nil
	case "github":
		return buildGitHub(decl, opts)
	default:
		return nil, fmt.Errorf("%w; accepted: %s", ErrUnknownType, strings.Join(Types(), ", "))
	}
}

// Types is what this deployment implements, named so a refusal can say what would be
// accepted rather than only what was not.
func Types() []string {
	types := []string{"discord", "slack", "github"}
	sort.Strings(types)
	return types
}

// CreatingTypes are the sink types that bring things into existence somewhere else, and
// so must declare a cap. The load gate asks rather than assuming: a list the gate
// hard-codes drifts from the one Build knows.
func CreatingTypes() []string { return []string{"github"} }

func buildGitHub(decl Declaration, opts BuildOptions) (Sink, error) {
	repo, err := resolved(decl, opts, "repo")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(repo, "/") {
		return nil, fmt.Errorf("repo %q is not owner/name", repo)
	}
	// The token may be absent for a deployment whose runner already carries one; what
	// must never happen is it being written into the playbook, which is why it is read
	// through the deployment like every other value.
	token, err := resolved(decl, opts, "token")
	if err != nil && decl.Config["token"] != nil {
		return nil, err
	}

	ceiling, declared := capOf(decl)
	if declared && ceiling <= 0 {
		return nil, fmt.Errorf("%w: %d creates nothing, which is not a ceiling", ErrNoCap, ceiling)
	}
	api, _ := decl.Config["api"].(string)
	return NewGitHub(repo, token, ceiling, declared, api, opts.Client), nil
}

// capOf reads the declared ceiling. YAML gives an int; JSON, and a value that came
// through a generic map, give a float64 — and a cap that silently read as absent because
// of which decoder produced it would be a bound lost to a type assertion.
func capOf(decl Declaration) (int, bool) {
	switch typed := decl.Config["cap"].(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func resolved(decl Declaration, opts BuildOptions, key string) (string, error) {
	raw, ok := decl.Config[key].(string)
	if !ok || raw == "" {
		return "", fmt.Errorf("no %s; accepted: a value or a reference such as ${config.%s}", key, key)
	}
	if opts.Interpolate == nil {
		return raw, nil
	}
	return opts.Interpolate(raw)
}

func webhookURL(decl Declaration, opts BuildOptions) (string, error) {
	raw, ok := decl.Config["webhook"].(string)
	if !ok || raw == "" {
		return "", fmt.Errorf("no webhook; accepted: a reference such as ${config.%s_webhook}", decl.Type)
	}
	if opts.Interpolate == nil {
		return raw, nil
	}
	resolved, err := opts.Interpolate(raw)
	if err != nil {
		return "", err
	}
	if resolved == "" {
		return "", fmt.Errorf("the webhook resolved to nothing")
	}
	return resolved, nil
}
