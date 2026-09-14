package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// EnvPrefix is what a bound reference becomes in the step's environment. A gather step
// is a shell line, so the value has to reach it as a variable rather than as text.
const EnvPrefix = "GRONIN_"

// Bound is a shell line whose references were replaced by variable expansions, and the
// environment that gives those variables their values.
type Bound struct {
	Line string
	Env  []string
}

// BindShell resolves the references in a gather step without ever putting a resolved
// value into the command's text.
//
// Textual substitution into a shell line is command injection by construction: a
// configuration value of `x; rm -rf ~` becomes a second command, and no amount of
// escaping in the value's writer helps, because the value is data the deployment holds
// and the playbook cannot see. So `${config.checkout}` becomes `"$GRONIN_CONFIG_checkout"`
// and the value is handed to the step through its environment. A double-quoted variable
// expansion is not re-parsed by the shell, so the value can be any byte string and still
// arrives as exactly one word.
//
// That containment only holds where the substitution controls its own quoting, which is
// why a reference inside a quoted region of the line is refused rather than bound: inside
// single quotes the expansion would not happen at all and the author would silently get
// the literal text, and inside double quotes it would nest and split on whitespace.
//
// FR-008 still holds: the environment a step ends up with is this returned list plus what
// the deployment already passes, never this process's own.
func (c *Config) BindShell(line string, trigger map[string]string) (Bound, error) {
	problems := unterminated(line)
	names := map[string]string{}
	values := map[string]string{}

	// Walked by offset rather than through ReplaceAllStringFunc: the quoting decision
	// needs where this reference is, and a callback is handed only the text it matched —
	// so a line referencing the same key twice would have both occurrences judged at the
	// position of the first.
	var out strings.Builder
	last := 0
	for _, span := range reference.FindAllStringSubmatchIndex(line, -1) {
		at, end := span[0], span[1]
		name := line[span[2]:span[3]]
		out.WriteString(line[last:at])
		last = end

		if quoted(line, at) {
			problems = append(problems, fmt.Errorf(
				"${%s} sits inside quotes; accepted: the reference on its own, as in "+
					"`git -C ${config.checkout} log` — it is substituted as one quoted "+
					"word and cannot be nested in another", name))
			out.WriteString(line[at:end])
			continue
		}
		namespace, key, found := strings.Cut(name, ".")
		if !found {
			problems = append(problems, fmt.Errorf(
				"${%s} does not name its source; accepted: ${config.%s} or ${trigger.%s}",
				name, name, name))
			out.WriteString(line[at:end])
			continue
		}

		value, err := c.resolve(namespace, key, name, trigger)
		if err != nil {
			problems = append(problems, err)
			out.WriteString(line[at:end])
			continue
		}
		variable := EnvPrefix + strings.ToUpper(namespace) + "_" + envSafe(key)
		if first, taken := names[variable]; taken && first != name {
			problems = append(problems, fmt.Errorf(
				"${%s} and ${%s} both bind to %s; accepted: keys that differ by more "+
					"than a dash or an underscore", first, name, variable))
			out.WriteString(line[at:end])
			continue
		}
		names[variable] = name
		values[variable] = value
		out.WriteString(`"$` + variable + `"`)
	}
	out.WriteString(line[last:])
	bound := out.String()

	if len(problems) > 0 {
		return Bound{}, errors.Join(problems...)
	}

	env := make([]string, 0, len(values))
	for variable, value := range values {
		env = append(env, variable+"="+value)
	}
	// Sorted so a run's record, and a replay of it, read the same twice.
	sort.Strings(env)
	return Bound{Line: bound, Env: env}, nil
}

func (c *Config) resolve(namespace, key, name string, trigger map[string]string) (string, error) {
	switch namespace {
	case "config":
		if value, ok := c.values[key]; ok {
			return value.Value, nil
		}
		return "", fmt.Errorf(
			"${config.%s} is not configured; set it with `printf '%%s' \"$VALUE\" | gronin config set %s`",
			key, key)
	case "trigger":
		if value, ok := trigger[key]; ok {
			return value, nil
		}
		return "", fmt.Errorf("${trigger.%s} is not in the trigger payload", key)
	default:
		return "", fmt.Errorf(
			"${%s} names %q, which is not a source; accepted: config, trigger", name, namespace)
	}
}

// envSafe turns a configuration key into a shell variable name. A key may carry a dash
// and a variable name may not; two keys that collide once the dash is gone are refused
// above rather than one of them silently winning.
func envSafe(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

// quoted reports whether the byte at an offset is inside a quoted region of a POSIX
// shell line.
//
// Exact rather than approximate, which single quotes make possible: inside them nothing
// escapes and nothing nests, so the state is a two-way toggle. Outside them a backslash
// escapes the next byte, and inside double quotes it does too.
func quoted(line string, at int) bool {
	var single, double bool
	for i := 0; i < at && i < len(line); i++ {
		switch line[i] {
		case '\\':
			if !single {
				i++
			}
		case '\'':
			if !double {
				single = !single
			}
		case '"':
			if !single {
				double = !double
			}
		}
	}
	return single || double
}

// QuotedReferences names the references in a shell line that sit inside quotes.
//
// Separated from BindShell so the load gate can refuse them before anything is armed:
// the gate holds no configuration values and does not need any to answer this, and a
// step refused at run time is refused after the schedule has already fired.
func QuotedReferences(line string) []string {
	var names []string
	for _, span := range reference.FindAllStringSubmatchIndex(line, -1) {
		if quoted(line, span[0]) {
			names = append(names, line[span[2]:span[3]])
		}
	}
	return names
}

// TriggerReferences names the payload values a gather line references — every
// ${trigger.x}, whether or not it sits inside quotes. The load gate uses it to refuse a
// gather step naming an undeclared value (FR-321), and playbook.Check uses it to apply
// FR-326's dash rule only to the values a gather step actually binds into a command.
func TriggerReferences(line string) []string {
	var names []string
	for _, match := range reference.FindAllStringSubmatch(line, -1) {
		namespace, key, found := strings.Cut(match[1], ".")
		if found && namespace == "trigger" {
			names = append(names, key)
		}
	}
	return names
}
