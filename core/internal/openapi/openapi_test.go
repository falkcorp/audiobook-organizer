// file: core/internal/openapi/openapi_test.go
// version: 1.0.0
// guid: 9a742d46-93fb-4f19-af76-dc05e141861b
// last-edited: 2026-11-07

package openapi_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/falkcorp/mediacore/internal/openapi"
	"github.com/falkcorp/mediacore/internal/openapi/openapitest"
)

func spec() openapi.Spec {
	return openapi.Spec{
		Title: "batcave", Version: "1.0.0", CookieName: "batcave_session",
		ProblemSchema: "core.Problem",
		Schemas: map[string]*jsonschema.Schema{
			"core.Problem":    {Type: "object"},
			"cave.Gadget":     {Type: "object", Properties: map[string]*jsonschema.Schema{"name": {Type: "string"}}},
			"cave.GadgetBody": {Type: "object"},
		},
		Operations: []openapi.Operation{
			{Method: "POST", Path: "/api/cave/v1/gadgets", OperationID: "cave_createGadget",
				Permission: "cave:edit", RequestBody: "cave.GadgetBody", SuccessStatus: 201, Success: "cave.Gadget"},
			{Method: "GET", Path: "/api/cave/v1/gadgets/{id}", OperationID: "cave_getGadget",
				Permission: "public", Public: true, Success: "cave.Gadget",
				Parameters: []openapi.Parameter{{Name: "id", In: "path", Schema: &jsonschema.Schema{Type: "string"}}}},
			{Method: "DELETE", Path: "/api/cave/v1/gadgets/{id}", OperationID: "cave_deleteGadget",
				Permission: "cave:admin", Guards: []string{"interactive"}, SuccessStatus: 204,
				Parameters: []openapi.Parameter{{Name: "id", In: "path", Schema: &jsonschema.Schema{Type: "string"}}}},
		},
	}
}

func TestBuild_ValidOpenAPI31(t *testing.T) {
	doc, err := openapi.Build(spec())
	if err != nil {
		t.Fatal(err)
	}
	b, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := openapitest.Validate(b); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	s := string(b)
	for _, want := range []string{
		`"openapi": "3.1.0"`,
		`"x-mediacore-permission": "public"`,
		`"x-mediacore-guards": [`,
		`"security": []`,
		`"$ref": "#/components/schemas/core.Problem"`,
		`"name": "batcave_session"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("document lacks %s:\n%s", want, s)
		}
	}
}

func TestBuild_Deterministic(t *testing.T) {
	a, err := openapi.Build(spec())
	if err != nil {
		t.Fatal(err)
	}
	s := spec()
	s.Operations[0], s.Operations[2] = s.Operations[2], s.Operations[0]
	b, err := openapi.Build(s)
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := a.JSON()
	jb, _ := b.JSON()
	if string(ja) != string(jb) {
		t.Fatal("operation order changed the document")
	}
}

func TestBuild_Refusals(t *testing.T) {
	for name, mut := range map[string]func(*openapi.Spec){
		"duplicate operationId": func(s *openapi.Spec) { s.Operations[1].OperationID = s.Operations[0].OperationID },
		"missing success":       func(s *openapi.Spec) { s.Operations[1].Success = "cave.Joker" },
		"missing body":          func(s *openapi.Spec) { s.Operations[0].RequestBody = "cave.Riddle" },
		"missing problem":       func(s *openapi.Spec) { s.ProblemSchema = "core.Nope" },
		"bad component name":    func(s *openapi.Spec) { s.Schemas["cave Gadget"] = &jsonschema.Schema{} },
		"duplicate method+path": func(s *openapi.Spec) {
			s.Operations[2].Method = "GET"
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := spec()
			mut(&s)
			if _, err := openapi.Build(s); !errors.Is(err, openapi.ErrInvalid) {
				t.Fatalf("Build error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestValidate_RefusesInvalidDocument(t *testing.T) {
	if err := openapitest.Validate([]byte(`{"openapi":"3.1.0","info":{"title":"x"}}`)); err == nil {
		t.Fatal("a document without info.version validated")
	}
}
