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
	default:
		return nil, fmt.Errorf("%w; accepted: %s", ErrUnknownType, strings.Join(Types(), ", "))
	}
}

// Types is what this deployment implements, named so a refusal can say what would be
// accepted rather than only what was not.
func Types() []string {
	types := []string{"discord", "slack"}
	sort.Strings(types)
	return types
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
