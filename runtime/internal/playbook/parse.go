package playbook

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// Schema is the published playbook schema, embedded so the executable can hand it out
// without the specification directory being present.
//
// It is the shape layer and it is NOT the gate. The refusals that matter — a shell in a
// tool set, a path that resolves outside the working directory, an MCP server this
// deployment does not provide — cannot be expressed in JSON Schema and live in the
// validator. A document this accepts may still be refused, by design.
//
//go:embed playbook.schema.json
var Schema []byte

// Parse reads one playbook document. It is the shape layer only: the document is checked
// against the published schema and decoded into typed structs, and nothing here decides
// whether the playbook is safe.
func Parse(path string, document []byte) (*Playbook, error) {
	var raw any
	if err := yaml.Unmarshal(document, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%s: the document is empty", path)
	}

	if err := ValidateShape(raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	var playbook Playbook
	if err := yaml.Unmarshal(document, &playbook); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	playbook.Path = path
	return &playbook, nil
}

// ParseFile reads and parses one playbook from disk.
func ParseFile(path string) (*Playbook, error) {
	document, err := os.ReadFile(path) //nolint:gosec // the caller names the playbook directory
	if err != nil {
		return nil, err
	}
	return Parse(path, document)
}

// PromptPath is where the agent's prompt lives, resolved relative to the playbook.
func (p *Playbook) PromptPath() string {
	if filepath.IsAbs(p.Agent.PromptFile) {
		return p.Agent.PromptFile
	}
	return filepath.Join(filepath.Dir(p.Path), p.Agent.PromptFile)
}

var compiled = func() *jsonschema.Schema {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(Schema))
	if err != nil {
		panic("the embedded playbook schema is not JSON: " + err.Error())
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("playbook.schema.json", document); err != nil {
		panic("the embedded playbook schema does not compile: " + err.Error())
	}
	schema, err := compiler.Compile("playbook.schema.json")
	if err != nil {
		panic("the embedded playbook schema does not compile: " + err.Error())
	}
	return schema
}()

// ShapeError is what the shape layer refused, one entry per failing location.
//
// The library's own message is a tree rooted at the schema, which reads as
// "validation failed with playbook.schema.json#" and names no field. A refusal has to
// name the field (FR-002), so the output is rendered here rather than passed on.
type ShapeError struct {
	Problems []ShapeProblem
}

// ShapeProblem is one failing location within a document.
type ShapeProblem struct {
	Location string // where in the document, e.g. "agent" or "sinks/0"
	Message  string
}

func (e *ShapeError) Error() string {
	lines := make([]string, 0, len(e.Problems))
	for _, problem := range e.Problems {
		where := problem.Location
		if where == "" {
			where = "the document"
		}
		lines = append(lines, where+": "+problem.Message)
	}
	return "does not match the playbook schema:\n  " + strings.Join(lines, "\n  ")
}

// ValidateShape checks a decoded document against the published schema.
func ValidateShape(document any) error {
	// YAML decodes mappings with `any` keys; JSON Schema needs string-keyed maps.
	normalised, err := normalise(document)
	if err != nil {
		return err
	}

	err = compiled.Validate(normalised)
	if err == nil {
		return nil
	}
	var invalid *jsonschema.ValidationError
	if !errors.As(err, &invalid) {
		return err
	}

	shape := &ShapeError{}
	for _, unit := range invalid.BasicOutput().Errors {
		message := unit.Error.String()
		// The root unit restates that something below it failed, which is the part that
		// names no field. Its children are the answer.
		if message == "" || strings.HasPrefix(message, "doesn't validate with") {
			continue
		}
		shape.Problems = append(shape.Problems, ShapeProblem{
			Location: strings.TrimPrefix(unit.InstanceLocation, "/"),
			Message:  message,
		})
	}
	if len(shape.Problems) == 0 {
		shape.Problems = append(shape.Problems, ShapeProblem{Message: invalid.Error()})
	}
	return shape
}

func normalise(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			converted, err := normalise(item)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			name, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("a key is %T, and a playbook's keys are strings", key)
			}
			converted, err := normalise(item)
			if err != nil {
				return nil, err
			}
			out[name] = converted
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			converted, err := normalise(item)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	default:
		return value, nil
	}
}
