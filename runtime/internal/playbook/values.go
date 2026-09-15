package playbook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nicodarge/Gronin/runtime/internal/config"
)

// Extract reads every value a webhook trigger declares out of a delivery's body, decoded
// with number literals kept: a JSON number survives as the text the sender wrote, never
// re-rendered from a decoded float, so two identities that differ only past a float's
// precision stay distinct (FR-316, which every declared value is taken under).
//
// It never fails. A body that is not JSON, or holds no object at its root, decodes to a
// nil document, and every declared pointer then resolves to nothing — an absent value,
// which Check refuses, not a parse failure (spec.md, *Edge Cases*).
func Extract(trigger Trigger, body []byte) map[string]any {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	_ = decoder.Decode(&decoded)

	values := make(map[string]any, len(trigger.Values))
	for name, declared := range trigger.Values {
		values[name] = AtPointer(decoded, declared.At)
	}
	return values
}

// AtPointer resolves an RFC 6901 JSON Pointer against an already-decoded document. A
// segment that does not resolve, on any shape it meets, returns nil — the same value a
// resolved JSON null decodes to, which is what Check reads as absent either way. The one
// path every caller resolves a body pointer through (plan.md, *Path Conventions*): a
// declared trigger value here, and a delivery's identity in internal/ingress/identity.go.
func AtPointer(document any, pointer string) any {
	if pointer == "" {
		return document
	}
	current := document
	for _, segment := range strings.Split(pointer, "/")[1:] {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			value, found := node[segment]
			if !found {
				return nil
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil
			}
			current = node[index]
		default:
			return nil
		}
	}
	return current
}

// ValueRefusalKind classifies why one declared value failed Check, so a caller that
// records it (internal/ingress/handoff.go) can name the reason without parsing prose out
// of a message meant for a human.
type ValueRefusalKind string

// Every reason Check refuses a declared value for, in the order FR-322 and FR-326 list
// them.
const (
	ValueAbsent      ValueRefusalKind = "absent"
	ValueNotSingle   ValueRefusalKind = "not_single"
	ValueTooLong     ValueRefusalKind = "too_long"
	ValueNoMatch     ValueRefusalKind = "no_match"
	ValueLeadingDash ValueRefusalKind = "leading_dash"
)

// ValueRefusal is one declared value Check would not accept.
type ValueRefusal struct {
	Name    string
	Kind    ValueRefusalKind
	Problem Problem
}

// Check applies FR-322's shape to every value a webhook trigger declares, and FR-326's
// dash rule to the ones a gather step references, and returns the checked values as
// strings when every one passes.
//
// values is what Extract returned for a delivery's body, or the strings an operator
// supplied on the command line, each wrapped as `any` so the two paths are held to the
// same rule (FR-327): a name --trigger did not carry reads exactly as absent as a
// declared pointer that resolved to nothing.
//
// It never stops at the first problem: an author or a sender fixing one value at a time
// is one round trip too many, and a delivery with several bad values should say so once.
func Check(book *Playbook, values map[string]any) (map[string]string, []ValueRefusal) {
	referenced := gatherReferenced(book)

	names := make([]string, 0, len(book.Trigger.Values))
	for name := range book.Trigger.Values {
		names = append(names, name)
	}
	sort.Strings(names)

	var refusals []ValueRefusal
	checked := make(map[string]string, len(names))

	for _, name := range names {
		declared := book.Trigger.Values[name]
		field := fmt.Sprintf("%s: trigger.values.%s", book.Name, name)
		raw, present := values[name]

		if !present || raw == nil {
			refusals = append(refusals, ValueRefusal{Name: name, Kind: ValueAbsent, Problem: Problem{
				Field: field, Found: "absent",
				Accepted: "a single value at " + declared.At,
			}})
			continue
		}

		text, single := SingleValue(raw)
		if !single {
			refusals = append(refusals, ValueRefusal{Name: name, Kind: ValueNotSingle, Problem: Problem{
				Field:    field,
				Found:    fmt.Sprintf("%q is not a single value (%T)", name, raw),
				Accepted: "a JSON string, number or boolean",
			}})
			continue
		}

		if length := utf8.RuneCountInString(text); length > declared.MaxLength {
			refusals = append(refusals, ValueRefusal{Name: name, Kind: ValueTooLong, Problem: Problem{
				Field: field,
				Found: fmt.Sprintf("%d Unicode code points, and the declared limit is %d",
					length, declared.MaxLength),
				Accepted: fmt.Sprintf("at most %d Unicode code points", declared.MaxLength),
			}})
			continue
		}

		if matched, err := anchoredMatch(declared.Pattern, text); err != nil {
			// The gate refuses a pattern that does not compile before this is ever
			// reached (validateWebhook). Reached anyway, this is refused rather than
			// left to panic on a value it cannot judge.
			refusals = append(refusals, ValueRefusal{Name: name, Kind: ValueNoMatch, Problem: Problem{
				Field: field, Found: fmt.Sprintf("its pattern %q does not compile: %v", declared.Pattern, err),
				Accepted: "a valid RE2 pattern",
			}})
			continue
		} else if !matched {
			refusals = append(refusals, ValueRefusal{Name: name, Kind: ValueNoMatch, Problem: Problem{
				Field:    field,
				Found:    fmt.Sprintf("%q does not wholly match its declared pattern %q", text, declared.Pattern),
				Accepted: "a value wholly matching " + declared.Pattern,
			}})
			continue
		}

		if referenced[name] && strings.HasPrefix(text, "-") {
			refusals = append(refusals, ValueRefusal{Name: name, Kind: ValueLeadingDash, Problem: Problem{
				Field:    field,
				Found:    fmt.Sprintf("%q begins with a dash and is bound into a gather step", text),
				Accepted: "a value that does not begin with -; a gather step would read it as an option",
			}})
			continue
		}

		checked[name] = text
	}

	if len(refusals) > 0 {
		return nil, refusals
	}
	return checked, nil
}

// SingleValue reads FR-316's single value out of a decoded JSON value: a string, a
// number kept as the literal text it was written as, or a boolean. Anything else —
// an object, an array, or nil, which stands for both a missing pointer and a JSON
// null — is not one. The one path every caller reads a single value through, a
// delivery's identity included (internal/ingress/identity.go).
func SingleValue(raw any) (string, bool) {
	switch typed := raw.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

// anchoredMatch applies FR-322: the pattern a trigger declares is what the whole value
// must match, so it is anchored here rather than trusted to already be — an unanchored
// pattern accepts a partial match, which is a different rule from the one declared.
func anchoredMatch(pattern, text string) (bool, error) {
	compiled, err := regexp.Compile(`^(?:` + pattern + `)$`)
	if err != nil {
		return false, err
	}
	return compiled.MatchString(text), nil
}

// gatherReferenced is every declared value name at least one gather step references, so
// Check can apply FR-326's dash rule only to the ones that would actually reach a shell
// command.
func gatherReferenced(book *Playbook) map[string]bool {
	referenced := map[string]bool{}
	for _, step := range book.Gather {
		for _, name := range config.TriggerReferences(step.Run) {
			referenced[name] = true
		}
	}
	return referenced
}
