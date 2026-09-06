package sink

import (
	"fmt"
	"net/http"
	"regexp"
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

// repoName is a safe superset of what GitHub allows — not its naming rules, which this
// does not try to reproduce. What it does guarantee is that nothing here can carry a
// query, a fragment or an extra path segment into the endpoint it is interpolated into.
var repoName = regexp.MustCompile(
	`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?/[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// CreatingTypes are the sink types that bring things into existence somewhere else, and
// so must declare a cap. The load gate asks rather than assuming: a list the gate
// hard-codes drifts from the one Build knows.
func CreatingTypes() []string { return []string{"github"} }

func buildGitHub(decl Declaration, opts BuildOptions) (Sink, error) {
	repo, err := resolved(decl, opts, "repo")
	if err != nil {
		return nil, err
	}
	// Checked against the shape rather than for a slash. It is interpolated into a URL,
	// and "owner/name?state=closed" contains a slash too.
	if !repoName.MatchString(repo) {
		return nil, fmt.Errorf("repo %q is not owner/name", repo)
	}
	// The token may be absent for a deployment whose runner already carries one; what
	// must never happen is it being written into the playbook, which is why it is read
	// through the deployment like every other value.
	//
	// Declared-and-empty is not absent. `gronin config set github_token ""` resolves to
	// an empty string without error, and accepting that built an unauthenticated client
	// with nothing said at load time — a playbook that looks configured and is not.
	var token string
	if _, declared := decl.Config["token"]; declared {
		token, err = resolved(decl, opts, "token")
		if err != nil {
			return nil, err
		}
		if token == "" {
			return nil, fmt.Errorf("token resolves to nothing; accepted: a value, or " +
				"remove the field for a deployment whose runner carries its own")
		}
	}

	// The label the cap is counted against. A playbook may name its own, because two
	// playbooks opening issues on one repository otherwise share a cap and the busier one
	// silences the other.
	//
	// A comma is refused rather than escaped. GitHub reads `labels=` as a list, so
	// "a,b" would count the issues carrying BOTH while creating one issue whose single
	// label is the literal "a,b" — the count and the creation would name different
	// things, and the cap would be measured against a set the sink never adds to.
	// Declared-and-empty is refused for the neighbouring reason: it drops the filter and
	// counts every open issue in the repository.
	label := Marker
	if raw, declared := decl.Config["label"]; declared {
		text, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("label is not a string; accepted: a label name, e.g. %q", Marker)
		}
		// Checked on the written text, before it resolves: the cap is counted against the
		// label, so whatever names it chooses the bucket the ceiling applies to, and a
		// trigger naming it makes the cap per-trigger rather than per-repository. The
		// load gate refuses this too — it is duplicated here for the same reason the
		// comma and the empty label are, so the sink holds the rule even when it is
		// reached by something that did not come through the gate.
		if strings.Contains(text, "${trigger.") {
			return nil, fmt.Errorf("label %q resolves through the trigger, which would let "+
				"what fires the run choose the bucket its cap is counted against; accepted: "+
				"a label name, or ${config.x}", text)
		}
		label, err = opts.interpolate(text)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(label) == "" {
			return nil, fmt.Errorf("label resolves to nothing; accepted: a label name, or " +
				"remove the field to use the default")
		}
		if strings.Contains(label, ",") {
			return nil, fmt.Errorf("label %q holds a comma, which GitHub reads as two labels; "+
				"accepted: one label name", label)
		}
	}

	ceiling, declared := capOf(decl)
	if declared && ceiling <= 0 {
		return nil, fmt.Errorf("%w: %d creates nothing, which is not a ceiling", ErrNoCap, ceiling)
	}
	// Resolved like every other value a playbook holds. It was read raw, so a playbook
	// writing `api: ${config.github_api}` passed the gate — which walks the field and
	// checks the key exists — and then sent its request to a URL still spelling the
	// reference. The sink is built before the agent, but the endpoint is only used at
	// delivery, so the failure landed after the run had been paid for.
	api, err := opts.interpolate(stringAt(decl, "api"))
	if err != nil {
		return nil, err
	}
	return NewGitHub(repo, token, label, ceiling, declared, api, opts.Client), nil
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

// interpolate resolves a value through the deployment, or returns it unchanged when a
// caller supplied no resolver. resolved() does this for a field it also requires; a label
// is optional, so the two halves are separate.
func (o BuildOptions) interpolate(raw string) (string, error) {
	if o.Interpolate == nil {
		return raw, nil
	}
	return o.Interpolate(raw)
}

// stringAt reads an optional string field, absent or wrongly typed reading as empty —
// which is what the sink already treated a missing endpoint as.
func stringAt(decl Declaration, key string) string {
	text, _ := decl.Config[key].(string)
	return text
}
