// file: core/outbox/schema.go
// version: 1.0.0
// guid: 8c0c0ae0-1a6a-45d2-9c60-3f3f64a2a7a9
// last-edited: 2026-11-12

package outbox

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// schemaCheck validates a payload against a topic's declared JSON Schema, the
// contract a raw consumer reads through (05-events §5E.4, §5E.7).
type schemaCheck struct{ res *jsonschema.Resolved }

// newSchemaCheck resolves raw; an empty schema checks nothing (nil).
func newSchemaCheck(raw json.RawMessage) (*schemaCheck, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("outbox: topic schema: %w", err)
	}
	res, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("outbox: topic schema: %w", err)
	}
	return &schemaCheck{res: res}, nil
}

// validate reports why payload does not satisfy the schema.
func (c *schemaCheck) validate(payload json.RawMessage) error {
	var inst any
	if err := json.Unmarshal(payload, &inst); err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	return c.res.Validate(inst)
}
