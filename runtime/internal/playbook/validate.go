package playbook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nicodarge/Gronin/runtime/internal/config"
)

// Deployment is what this deployment can actually do. The gate refuses a playbook naming
// anything outside it, because a name that survives the gate fails at delivery instead —
// after a full agent run has been paid for.
type Deployment struct {
	// MCPServers this deployment provides.
	MCPServers []string
	// SinkTypes it implements.
	SinkTypes []string
	// CreatingSinks are the types that bring things into existence somewhere else, and
	// so must declare a cap.
	CreatingSinks []string
	// ConfigKeys are the configuration keys this deployment holds. A ${config.x} naming
	// anything else is refused here, which is what spec.md's "refused at load, not at
	// trigger time" asks for: without this the gate accepts a playbook whose every
	// reference resolves to nothing, and the operator finds out at the first trigger.
	//
	// The values are deliberately absent. The gate decides whether a name resolves, and
	// a gate holding the deployment's secrets is a gate that leaks them into a refusal.
	ConfigKeys []string
}

// Problem is one refusal: where it is, what was found, and what would be accepted.
//
// The last field is the one that matters. A gate that says only what is wrong makes the
// author guess, and a gate that is guessed at gets switched off.
type Problem struct {
	Field    string
	Found    string
	Accepted string
}

func (p Problem) Error() string {
	if p.Accepted == "" {
		return fmt.Sprintf("%s: %s", p.Field, p.Found)
	}
	return fmt.Sprintf("%s: %s\n    accepted: %s", p.Field, p.Found, p.Accepted)
}

// readOnlyTools is the built-in set a playbook may name.
//
// An allowlist, not a denylist. A denylist has to enumerate every dangerous form, and the
// one it misses is the one that ships; this refuses a tool nobody has thought about yet,
// which is the safe direction to be wrong in. Adding to it is a deliberate act.
var readOnlyTools = map[string]string{
	"Read":      "reads files",
	"Grep":      "searches files",
	"Glob":      "lists files",
	"WebFetch":  "fetches a URL",
	"WebSearch": "searches the web",
}

// shells are named separately so the refusal can say what a tool IS rather than only
// that it is not on the list. "Bash is not accepted" sends an author looking for a
// spelling; "Bash is an unrestricted shell" tells them why it never will be.
var shells = map[string]bool{
	"Bash": true, "Shell": true, "PowerShell": true, "Zsh": true, "Sh": true,
	"BashOutput": true, "KillShell": true,
}

var writingTools = map[string]bool{
	"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true,
	"Task": true, "Agent": true,
}

// mcpShape reads an MCP entry: whether it names a server, and whether it names a tool
// inside one.
//
// Split rather than matched. The regex this replaced was `^mcp__[A-Za-z0-9_-]+$` for
// "names a whole server", and `_` is in that class — so it matched a fully-qualified
// tool too, and the gate refused `mcp__grafana__query_prometheus` for naming a whole
// server. A character class that quietly includes the separator is the same class of
// mistake as a pattern that reads as containment and is not.
func mcpShape(entry string) (server string, wholeServer bool, isMCP bool) {
	parts := strings.Split(entry, "__")
	if len(parts) < 2 || parts[0] != "mcp" || parts[1] == "" {
		return "", false, false
	}
	// What decides is whether a TOOL follows the server, not how many parts there are.
	// Counting parts let mcp__grafana__ through — split gives ["mcp","grafana",""],
	// three parts, which read as "a tool". Requiring the remainder to be empty then let
	// mcp__grafana____ through, whose remainder is "__". Naming the accepted shape is
	// the only version of this that does not need another empty form to be thought of.
	if !toolName.MatchString(strings.Join(parts[2:], "__")) {
		return parts[1], true, true
	}
	return parts[1], false, true
}

// toolName is what a tool inside an MCP server may be called. An allowlist for the same
// reason the tool set uses one: the shapes that are not names are not worth enumerating.
var toolName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// scopedTool matches `Read(./**)` — a file tool with a path scope.
var scopedTool = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)\((.*)\)$`)

// bareReference matches an interpolation that names no source.
var bareReference = regexp.MustCompile(`\$\{([^}.]*)\}`)

// Validate applies every refusal rule and returns all of them.
//
// It never stops at the first. Fixing refusals one round trip at a time is how a gate
// gets switched off, and the caller refuses the whole set rather than arming the valid
// remainder.
func Validate(book *Playbook, dep Deployment) []Problem {
	var problems []Problem

	problems = append(problems, validateAgent(book, dep)...)
	problems = append(problems, validateSinks(book, dep)...)
	problems = append(problems, validateReserved(book)...)
	problems = append(problems, validateInterpolation(book, dep)...)
	return problems
}

func validateAgent(book *Playbook, dep Deployment) []Problem {
	var problems []Problem
	agent := book.Agent

	// FR-041. Turning off the coarsest bound is a decision, and a decision nobody wrote
	// down is indistinguishable from an accident on review.
	if !agent.IsRestricted() && strings.TrimSpace(book.Description) == "" {
		problems = append(problems, Problem{
			Field:    "agent.restricted",
			Found:    "false, with no description saying why",
			Accepted: "a description stating why this playbook needs the command- and code-running tools",
		})
	}

	// FR-003.
	for at, tool := range agent.Tools {
		field := fmt.Sprintf("agent.tools[%d]", at)
		switch {
		case shells[tool]:
			problems = append(problems, Problem{
				Field: field, Found: fmt.Sprintf("%q is an unrestricted shell", tool),
				Accepted: acceptedTools(),
			})
		case writingTools[tool]:
			problems = append(problems, Problem{
				Field:    field,
				Found:    fmt.Sprintf("%q can write outside the run's working directory", tool),
				Accepted: acceptedTools(),
			})
		case isMCPEntry(tool):
			problems = append(problems, mcpProblems(field, tool, dep)...)
		default:
			if _, known := readOnlyTools[tool]; !known {
				problems = append(problems, Problem{
					Field: field, Found: fmt.Sprintf("%q is not a tool this runtime accepts", tool),
					Accepted: acceptedTools(),
				})
			}
		}
	}

	// FR-004 and FR-005.
	for at, entry := range agent.Allow {
		field := fmt.Sprintf("agent.allow[%d]", at)
		switch {
		case isMCPEntry(entry):
			problems = append(problems, mcpProblems(field, entry, dep)...)
		default:
			problems = append(problems, scopeProblems(field, entry)...)
		}
	}

	// FR-007.
	for at, name := range agent.MCP {
		if !contains(dep.MCPServers, name) {
			problems = append(problems, Problem{
				Field:    fmt.Sprintf("agent.mcp[%d]", at),
				Found:    fmt.Sprintf("%q is not a server this deployment provides", name),
				Accepted: provided(dep.MCPServers),
			})
		}
	}

	// A prompt that is not there arms a playbook that cannot run. The gate is where that
	// is cheap to find.
	if agent.PromptFile != "" && book.Path != "" {
		body, err := readPrompt(book.PromptPath())
		if err != nil {
			problems = append(problems, Problem{
				Field:    "agent.prompt_file",
				Found:    fmt.Sprintf("%q %s", agent.PromptFile, promptReason(err)),
				Accepted: "a readable file beside the playbook, under 1 MiB",
			})
		} else {
			// The prompt body is the string the runtime actually interpolates against the
			// trigger payload, so it is where FR-039 most needs applying. The walk covered
			// the prompt's PATH and not its content, so a bare reference there was caught
			// at run time — after the gather steps had already run and cost money.
			where := "agent.prompt_file (" + agent.PromptFile + ")"
			problems = append(problems, bareReferences(where, string(body))...)
			problems = append(problems, unconfigured(where, string(body), configured(dep))...)
		}
	}

	if agent.Timeout != "" {
		if _, err := agent.StageTimeout(); err != nil {
			problems = append(problems, Problem{
				Field: "agent.timeout", Found: fmt.Sprintf("%q is not a duration", agent.Timeout),
				Accepted: "a duration such as 10m, 45m or 2h",
			})
		}
	}
	return problems
}

func isMCPEntry(entry string) bool {
	_, _, isMCP := mcpShape(entry)
	return isMCP
}

// promptReason renders why a prompt could not be used, so a refusal distinguishes "not
// there" from "too large" — which are different mistakes with different fixes.
func promptReason(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "is not there"
	}
	return err.Error()
}

// MaxPromptBytes bounds the prompt a playbook may carry. Every other read in this
// runtime is bounded — gather output, a sink's response — and this one was not: a
// prompt_file pointing at something unexpectedly large, or at a file that never ends,
// was read whole before any other check ran.
const MaxPromptBytes int64 = 1 << 20

func readPrompt(path string) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // the path the playbook names, beside it
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("is not a regular file")
	}
	if info.Size() > MaxPromptBytes {
		return nil, fmt.Errorf("is %d bytes, and a prompt may be at most %d",
			info.Size(), MaxPromptBytes)
	}
	return io.ReadAll(io.LimitReader(file, MaxPromptBytes))
}

// mcpProblems refuses an entry naming a whole server (FR-004), and one naming a server
// this deployment does not provide (FR-007). Naming a tool is not the same as naming the
// server, and a playbook can do the second without doing the first.
func mcpProblems(field, entry string, dep Deployment) []Problem {
	server, wholeServer, _ := mcpShape(entry)
	if wholeServer {
		return []Problem{{
			Field: field, Found: fmt.Sprintf("%q names a whole MCP server", entry),
			Accepted: "an individual tool, e.g. " + entry + "__query_prometheus",
		}}
	}
	if !contains(dep.MCPServers, server) {
		return []Problem{{
			Field:    field,
			Found:    fmt.Sprintf("%q names the server %q, which this deployment does not provide", entry, server),
			Accepted: provided(dep.MCPServers),
		}}
	}
	return nil
}

// scopeProblems applies FR-005: a file tool's scope must resolve inside the run's working
// directory, and the path is resolved before the decision rather than inspected as text.
// A relative traversal reads as harmless until it is resolved.
func scopeProblems(field, entry string) []Problem {
	match := scopedTool.FindStringSubmatch(entry)
	if match == nil {
		// Not a scoped form. Bare tool names in the allowlist are the tool set's
		// business, and repeating that refusal here would say the same thing twice.
		if _, known := readOnlyTools[entry]; known {
			return nil
		}
		return []Problem{{
			Field: field, Found: fmt.Sprintf("%q is not a form this runtime accepts", entry),
			Accepted: `a scoped file tool such as Read(./**), or an MCP tool named in full`,
		}}
	}

	tool, scope := match[1], match[2]
	if _, known := readOnlyTools[tool]; !known {
		return []Problem{{
			Field: field, Found: fmt.Sprintf("%q scopes %q, which is not a tool this runtime accepts", entry, tool),
			Accepted: acceptedTools(),
		}}
	}
	if scope == "" {
		return []Problem{{
			Field: field, Found: fmt.Sprintf("%q scopes nothing", entry),
			Accepted: "a path inside the run's working directory, e.g. " + tool + "(./**)",
		}}
	}
	if !insideWorkingDirectory(scope) {
		return []Problem{{
			Field:    field,
			Found:    fmt.Sprintf("%q resolves outside the run's working directory", scope),
			Accepted: "a path inside it, e.g. " + tool + "(./**) or " + tool + "(./inputs/**)",
		}}
	}
	return nil
}

// insideWorkingDirectory resolves a scope the way the filesystem would, against a
// notional root, and reports whether it stayed inside. Text inspection is not enough:
// "./a/../../etc" reads as relative and is not.
func insideWorkingDirectory(scope string) bool {
	if filepath.IsAbs(scope) {
		return false
	}
	if strings.HasPrefix(scope, "~") {
		return false
	}
	const root = "/run"
	// The glob characters do not affect where the path resolves to, and Clean leaves
	// them alone.
	resolved := filepath.Clean(filepath.Join(root, scope))
	return resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator))
}

func validateSinks(book *Playbook, dep Deployment) []Problem {
	var problems []Problem
	for at, one := range book.Sinks {
		field := fmt.Sprintf("sinks[%d]", at)
		name, config, ok := one.Type()
		if !ok {
			found := "is not one key naming a sink type with its settings"
			if name != "" {
				found = fmt.Sprintf("%q does not carry a mapping of settings", name)
			}
			problems = append(problems, Problem{
				Field: field, Found: found,
				Accepted: "one key naming the sink, e.g. discord: {webhook: ${config.ops_webhook}}",
			})
			continue
		}
		// FR-038.
		if !contains(dep.SinkTypes, name) {
			problems = append(problems, Problem{
				Field:    field + "." + name,
				Found:    fmt.Sprintf("%q is not a sink this deployment implements", name),
				Accepted: provided(dep.SinkTypes),
			})
			continue
		}
		// FR-006.
		if contains(dep.CreatingSinks, name) {
			if _, declared := config["cap"]; !declared {
				problems = append(problems, Problem{
					Field: field + "." + name + ".cap", Found: "missing",
					Accepted: "an integer; a sink that creates things must declare its ceiling",
				})
			}
			problems = append(problems, labelProblems(field+"."+name+".label", config)...)
		}
	}
	return problems
}

// validateReserved applies FR-034. The schema refuses these too; this is the same rule
// where the runtime can say why, and it holds if the schema is ever loosened.
func validateReserved(book *Playbook) []Problem {
	var problems []Problem
	for field, present := range map[string]bool{"guard": book.Guard != nil, "retrieve": book.Retrieve != nil} {
		if present {
			problems = append(problems, Problem{
				Field:    field,
				Found:    "declared, and this runtime does not apply it",
				Accepted: "remove the block; a declared bound nothing enforces reads as enforced in review",
			})
		}
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Field < problems[j].Field })
	return problems
}

// validateInterpolation applies FR-039 to every string the document holds. A bare
// reference resolves against whichever source happens to carry the name, and a trigger
// payload is written by whoever sent the request.
func validateInterpolation(book *Playbook, dep Deployment) []Problem {
	known := configured(dep)

	var problems []Problem
	for _, held := range interpolatable(book) {
		problems = append(problems, bareReferences(held.field, held.text)...)
		if resolvedHere[fieldKind(held.field)] {
			problems = append(problems, unconfigured(held.field, held.text, known)...)
		}
	}
	for at, step := range book.Gather {
		// A gather step's references are bound through its environment rather than
		// substituted into its text, and that containment is what a quoted reference
		// breaks. Refused here rather than at the run: a step refused at the run is
		// refused after the schedule has already fired.
		for _, name := range config.QuotedReferences(step.Run) {
			problems = append(problems, Problem{
				Field: fmt.Sprintf("gather[%d].run", at),
				Found: fmt.Sprintf("${%s} sits inside quotes", name),
				Accepted: "the reference on its own, as in `git -C ${config.checkout} log` — " +
					"it is substituted as one quoted word and cannot be nested in another",
			})
		}
	}
	return problems
}

// resolvedHere names the fields the runtime interpolates. Only those are held to
// resolving: a ${config.x} in a field nothing interpolates is broken whatever the
// deployment holds, and refusing it for the wrong reason sends the author to set a key
// that would not have helped.
var resolvedHere = map[string]bool{"gather.run": true, "sinks": true}

// fieldKind reduces "gather[0].run" and "sinks[1].github.repo" to the kind of field they
// are, which is what decides whether the runtime resolves them.
func fieldKind(field string) string {
	if at := strings.IndexByte(field, '['); at >= 0 {
		rest := field[at:]
		if end := strings.IndexByte(rest, ']'); end >= 0 {
			field = field[:at] + rest[end+1:]
		}
	}
	if before, _, found := strings.Cut(field, "."); found && before == "sinks" {
		return "sinks"
	}
	return strings.TrimPrefix(field, ".")
}

// unconfigured refuses a ${config.x} this deployment cannot resolve. ${trigger.x} is not
// checked and cannot be: the payload does not exist until something fires the run.
func unconfigured(field, text string, known map[string]bool) []Problem {
	var problems []Problem
	for _, match := range configReference.FindAllStringSubmatch(text, -1) {
		key := match[1]
		if known[key] {
			continue
		}
		problems = append(problems, Problem{
			Field:    field,
			Found:    fmt.Sprintf("${config.%s} is not configured", key),
			Accepted: fmt.Sprintf("set it with `gronin config set %s <value>`", key),
		})
	}
	return problems
}

var configReference = regexp.MustCompile(`\$\{config\.([^}]*)\}`)

func bareReferences(field, text string) []Problem {
	var problems []Problem
	for _, match := range bareReference.FindAllStringSubmatch(text, -1) {
		name := match[1]
		problems = append(problems, Problem{
			Field:    field,
			Found:    fmt.Sprintf("${%s} does not name its source", name),
			Accepted: fmt.Sprintf("${config.%s} or ${trigger.%s}", name, name),
		})
	}
	return problems
}

// held is one string in the document and where it is, so a refusal names the field
// rather than the document.
type held struct {
	field string
	text  string
}

// interpolatable walks every string a playbook holds. Walking the typed structure rather
// than the raw document means a field added later is not silently exempt from the rule —
// it has to be added here, which is a compile-time-shaped reminder rather than a silent
// hole.
//
// Three of these are interpolated by the runtime — the prompt body, checked where it is
// read above, a sink's configuration, and a gather step. resolvedHere below is which. The
// rest are checked for a bare reference anyway: an author who writes ${x} in a field
// nothing resolves means it to be resolved, and a gate that stays quiet teaches them it
// works.
func interpolatable(book *Playbook) []held {
	out := []held{
		{"description", book.Description},
		{"trigger.schedule", book.Trigger.Schedule},
		{"agent.model", book.Agent.Model},
		{"agent.prompt_file", book.Agent.PromptFile},
	}
	for at, step := range book.Gather {
		out = append(out,
			held{fmt.Sprintf("gather[%d].run", at), step.Run},
			held{fmt.Sprintf("gather[%d].as", at), step.As})
	}
	for at, tool := range book.Agent.Tools {
		out = append(out, held{fmt.Sprintf("agent.tools[%d]", at), tool})
	}
	for at, entry := range book.Agent.Allow {
		out = append(out, held{fmt.Sprintf("agent.allow[%d]", at), entry})
	}
	for at, name := range book.Agent.MCP {
		out = append(out, held{fmt.Sprintf("agent.mcp[%d]", at), name})
	}
	for at, one := range book.Sinks {
		name, config, ok := one.Type()
		if !ok {
			continue
		}
		out = append(out, walkValue(fmt.Sprintf("sinks[%d].%s", at, name), config)...)
	}
	return out
}

func walkValue(field string, value any) []held {
	switch typed := value.(type) {
	case string:
		return []held{{field, typed}}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var out []held
		for _, key := range keys {
			out = append(out, walkValue(field+"."+key, typed[key])...)
		}
		return out
	case Sink:
		return walkValue(field, map[string]any(typed))
	case []any:
		var out []held
		for at, item := range typed {
			out = append(out, walkValue(fmt.Sprintf("%s[%d]", field, at), item)...)
		}
		return out
	default:
		return nil
	}
}

func acceptedTools() string {
	names := make([]string, 0, len(readOnlyTools))
	for name := range readOnlyTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ") + ", or an MCP tool named in full (mcp__server__tool)"
}

func provided(names []string) string {
	if len(names) == 0 {
		return "nothing; this deployment provides none"
	}
	ordered := append([]string(nil), names...)
	sort.Strings(ordered)
	return strings.Join(ordered, ", ")
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// configured is what this deployment can resolve, as a set.
func configured(dep Deployment) map[string]bool {
	known := make(map[string]bool, len(dep.ConfigKeys))
	for _, key := range dep.ConfigKeys {
		known[key] = true
	}
	return known
}

// labelProblems applies the label rule where it is cheap: the cap is counted against the
// label, so a label the sink cannot use is a cap measured against nothing.
//
// The sink refuses these too, when it is built. Here as well because there the run has
// already been triggered, and this gate exists so a playbook that cannot work is refused
// before anything is armed. What this cannot judge is a reference — the deployment holds
// that value and the gate holds no values — so a resolved label is checked by the sink,
// and a written one is checked twice.
func labelProblems(field string, config map[string]any) []Problem {
	raw, declared := config["label"]
	if !declared {
		return nil
	}
	text, ok := raw.(string)
	if !ok {
		return []Problem{{
			Field: field, Found: "is not a string",
			Accepted: "a label name, e.g. doc-drift",
		}}
	}
	if strings.Contains(text, ",") {
		return []Problem{{
			Field: field,
			Found: fmt.Sprintf("%q holds a comma, which GitHub reads as two labels", text),
			Accepted: "one label name; the cap counts the issues carrying it, and a list " +
				"would count issues the sink never creates",
		}}
	}
	// A reference resolves to a value this gate cannot see, so only a written label is
	// judged empty here.
	if !strings.Contains(text, "${") && strings.TrimSpace(text) == "" {
		return []Problem{{
			Field: field, Found: "is empty, which counts every open issue in the repository",
			Accepted: "a label name, or remove the field to use the default",
		}}
	}
	return nil
}
