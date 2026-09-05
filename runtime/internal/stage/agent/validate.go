package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrReportInvalid is returned when the agent's answer does not satisfy the schema the
// playbook declared.
var ErrReportInvalid = errors.New("the agent report does not satisfy the declared output schema")

// ValidateReport checks the agent's answer against the playbook's declared schema.
//
// FR-014 marks the run failed when it does not conform, and what the sinks are given is
// the validation failure rather than the malformed content. A sink that posts whatever
// the agent said, when the runtime has just decided it cannot read it, is how a bad run
// becomes a bad action — which is exactly what Principle II exists to prevent.
func ValidateReport(report []byte, schema any) error {
	if schema == nil {
		return nil
	}
	compiled, err := compileSchema(schema)
	if err != nil {
		return err
	}

	if len(bytes.TrimSpace(report)) == 0 {
		return fmt.Errorf("%w: the agent answered with nothing", ErrReportInvalid)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(report))
	if err != nil {
		return fmt.Errorf("%w: the answer is not JSON: %w", ErrReportInvalid, err)
	}
	if err := compiled.Validate(document); err != nil {
		var invalid *jsonschema.ValidationError
		if errors.As(err, &invalid) {
			return fmt.Errorf("%w:%s", ErrReportInvalid, describe(invalid))
		}
		return fmt.Errorf("%w: %w", ErrReportInvalid, err)
	}
	return nil
}

func compileSchema(schema any) (*jsonschema.Schema, error) {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("the declared output schema cannot be encoded: %w", err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("the declared output schema is not JSON: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("output.schema.json", document); err != nil {
		return nil, fmt.Errorf("the declared output schema does not compile: %w", err)
	}
	compiled, err := compiler.Compile("output.schema.json")
	if err != nil {
		return nil, fmt.Errorf("the declared output schema does not compile: %w", err)
	}
	return compiled, nil
}

// describe renders the failing locations, the same way the playbook shape layer does.
// The library's own message names the schema and not the document, which tells the
// reader nothing about the answer they are looking at.
func describe(invalid *jsonschema.ValidationError) string {
	var out bytes.Buffer
	for _, unit := range invalid.BasicOutput().Errors {
		message := unit.Error.String()
		if message == "" || bytes.HasPrefix([]byte(message), []byte("doesn't validate with")) {
			continue
		}
		where := unit.InstanceLocation
		if where == "" {
			where = "the report"
		}
		fmt.Fprintf(&out, "\n  %s: %s", where, message)
	}
	if out.Len() == 0 {
		return " " + invalid.Error()
	}
	return out.String()
}
