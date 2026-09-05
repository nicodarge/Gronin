package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// reference matches an interpolation, namespace included or not. It deliberately
// matches the namespaceless form so the resolver can refuse it by name (FR-039) rather
// than leave it in the output looking like literal text.
var reference = regexp.MustCompile(`\$\{([^}]*)\}`)

// ValidKey reports whether a configuration key can be referenced at all. The shape is
// the one the reference syntax can express: anything else could never be resolved, so
// storing it would create a value no playbook can read.
func ValidKey(key string) error {
	if key == "" {
		return errors.New("a configuration key cannot be empty")
	}
	if !validKey.MatchString(key) {
		return fmt.Errorf("%q is not a valid configuration key; accepted: letters, digits, "+
			"underscore and dash, e.g. discord_webhook", key)
	}
	return nil
}

var validKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Interpolate resolves ${config.x} and ${trigger.x} in text.
//
// It resolves against these two sources and nothing else. The process environment is
// not one of them (FR-008): a playbook is data that has to run against any deployment
// providing what it declares, and exposing the environment to a template hands every
// playbook the deployment's own database password.
//
// A reference without a namespace is refused rather than resolved (FR-039). A bare
// ${x} would resolve against whichever source happened to hold the name, and a trigger
// payload is written by whoever sent the request.
//
// Every unresolved reference is reported, not just the first: fixing them one round
// trip at a time is how a gate gets switched off.
func (c *Config) Interpolate(text string, trigger map[string]string) (string, error) {
	var problems []error

	resolved := reference.ReplaceAllStringFunc(text, func(match string) string {
		name := reference.FindStringSubmatch(match)[1]
		namespace, key, found := strings.Cut(name, ".")
		if !found {
			problems = append(problems, fmt.Errorf(
				"${%s} does not name its source; accepted: ${config.%s} or ${trigger.%s}",
				name, name, name))
			return match
		}

		switch namespace {
		case "config":
			if value, ok := c.values[key]; ok {
				return value.Value
			}
			problems = append(problems, fmt.Errorf(
				"${config.%s} is not configured; set it with `gronin config set %s <value>`",
				key, key))
		case "trigger":
			if value, ok := trigger[key]; ok {
				return value
			}
			problems = append(problems, fmt.Errorf("${trigger.%s} is not in the trigger payload", key))
		default:
			problems = append(problems, fmt.Errorf(
				"${%s} names %q, which is not a source; accepted: config, trigger",
				name, namespace))
		}
		return match
	})

	if len(problems) > 0 {
		return "", errors.Join(problems...)
	}
	return resolved, nil
}
