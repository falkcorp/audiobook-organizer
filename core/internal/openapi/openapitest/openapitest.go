// file: core/internal/openapi/openapitest/openapitest.go
// version: 1.0.0
// guid: 71446900-8dbb-4779-b0c2-545c56420bdf
// last-edited: 2026-11-07

// Package openapitest validates a generated document against the OpenAPI 3.1
// meta-schema (05-http-api §12 TestOpenAPI_IsValid31). It is test support (03
// R4: a <pkg>/<pkg>test package, not layered), so the embedded meta-schema
// never reaches a binary.
//
// oas-3.1-schema-2022-10-07.json is the OpenAPI Initiative's published schema
// for OpenAPI 3.1 documents, https://spec.openapis.org/oas/3.1/schema/2022-10-07
// (Apache License 2.0), committed verbatim so the check needs no network.
package openapitest

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed oas-3.1-schema-2022-10-07.json
var metaSchema string //archtest:allow-var embedded test-support data, never written

// Validate reports whether doc (JSON) is a valid OpenAPI 3.1 document.
func Validate(doc []byte) error {
	var s jsonschema.Schema
	if err := json.Unmarshal([]byte(metaSchema), &s); err != nil {
		return fmt.Errorf("openapitest: meta-schema: %w", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		return fmt.Errorf("openapitest: resolve meta-schema: %w", err)
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return fmt.Errorf("openapitest: document: %w", err)
	}
	if err := rs.Validate(v); err != nil {
		return fmt.Errorf("openapitest: not a valid OpenAPI 3.1 document: %w", err)
	}
	return nil
}
