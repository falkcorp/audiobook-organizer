// file: core/internal/openapi/openapi.go
// version: 1.0.0
// guid: a2f1e261-1fe2-4b99-8096-2d1ff8cd1f88
// last-edited: 2026-11-07

// Package openapi builds core's OpenAPI 3.1 documents (05-http-api §7) from
// its own leaf operation description, Operation. httpapi imports it and fills
// Operations from its RouteInfo and the schemas it reflected through
// core/internal/jsonschemax; this package never imports httpapi (04 §8.1 R1,
// MOD3-4), so the two cannot form a cycle.
//
// The output is deterministic: every map is keyed and encoding/json writes
// map keys sorted, and Operations are placed by path and method, so the same
// routes give the same bytes (the freshness gate of core/openapi/core.json
// depends on it).
package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Version is the OpenAPI version every document declares.
const Version = "3.1.0"

// ErrInvalid is returned by Build for a Spec that cannot become a valid
// document: a duplicate operation ID or method+path, a reference to a schema
// the Spec does not carry, or a component name OpenAPI refuses.
var ErrInvalid = errors.New("openapi: invalid document spec")

// Spec is everything Build needs for one owner's document.
type Spec struct {
	Title   string // info.title
	Version string // info.version
	// CookieName is the name of the session cookie the sessionCookie scheme
	// names (05-authn §4.3: core.auth.session.cookie_name, default
	// "<AppID>_session").
	CookieName string
	// ProblemSchema is the component name of the problem+json body every
	// operation's default response references (httpapi's "core.Problem").
	ProblemSchema string
	// Schemas are the named components, already reflected.
	Schemas    map[string]*jsonschema.Schema
	Operations []Operation
}

// Operation is the leaf description of one route (05-http-api §7).
type Operation struct {
	Method      string // upper case, e.g. "GET"
	Path        string // OpenAPI path template, e.g. "/api/hello/v1/greeting/{id}"
	OperationID string // "<owner>_<Route.Name>"
	Summary     string
	Description string
	Tags        []string
	// Permission is x-mediacore-permission: the scope, or "public" /
	// "authenticated" (rbac.Requirement.String()).
	Permission string
	// Public marks a route that needs no credential: its security is the
	// empty list, which overrides the document default.
	Public bool
	// Guards is x-mediacore-guards ("interactive", "owner"); empty omits it.
	Guards     []string
	Parameters []Parameter
	// RequestBody is the component name of the JSON request body; "" = none.
	RequestBody string
	// SuccessStatus is the success status code (200, 201, 204).
	SuccessStatus int
	// Success is the component name of the success body; "" = no body.
	Success string
}

// Parameter is a path, query or header parameter.
type Parameter struct {
	Name     string
	In       string // "path", "query" or "header"
	Required bool
	Schema   *jsonschema.Schema
}

// Document is one OpenAPI 3.1 document, as core emits it.
type Document struct {
	OpenAPI    string                `json:"openapi"`
	Info       Info                  `json:"info"`
	Paths      map[string]PathItem   `json:"paths"`
	Components Components            `json:"components"`
	Security   []map[string][]string `json:"security"`
}

// Info is the document's info object.
type Info struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

// PathItem maps a lower-case HTTP method to its operation.
type PathItem map[string]*OperationObject

// OperationObject is the JSON form of one Operation.
type OperationObject struct {
	OperationID string                 `json:"operationId"`
	Summary     string                 `json:"summary,omitempty"`
	Description string                 `json:"description,omitempty"`
	Tags        []string               `json:"tags,omitempty"`
	Parameters  []ParameterObject      `json:"parameters,omitempty"`
	RequestBody *RequestBodyObject     `json:"requestBody,omitempty"`
	Responses   map[string]Response    `json:"responses"`
	Security    *[]map[string][]string `json:"security,omitempty"`
	Permission  string                 `json:"x-mediacore-permission"`
	Guards      []string               `json:"x-mediacore-guards,omitempty"`
}

// ParameterObject is the JSON form of one Parameter.
type ParameterObject struct {
	Name     string             `json:"name"`
	In       string             `json:"in"`
	Required bool               `json:"required,omitempty"`
	Schema   *jsonschema.Schema `json:"schema"`
}

// RequestBodyObject is a JSON request body.
type RequestBodyObject struct {
	Required bool                 `json:"required"`
	Content  map[string]MediaType `json:"content"`
}

// Response is one response object.
type Response struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// MediaType is one media type entry.
type MediaType struct {
	Schema *jsonschema.Schema `json:"schema"`
}

// Components holds the named schemas and the security schemes.
type Components struct {
	Schemas         map[string]*jsonschema.Schema `json:"schemas"`
	SecuritySchemes map[string]SecurityScheme     `json:"securitySchemes"`
}

// SecurityScheme is an OpenAPI security scheme.
type SecurityScheme struct {
	Type        string `json:"type"`
	In          string `json:"in,omitempty"`
	Name        string `json:"name,omitempty"`
	Scheme      string `json:"scheme,omitempty"`
	Description string `json:"description,omitempty"`
}

// componentName is OpenAPI's component key rule.
func componentName() *regexp.Regexp { return regexp.MustCompile(`^[a-zA-Z0-9._-]+$`) }

// Ref returns the schema that references a component by name.
func Ref(name string) *jsonschema.Schema {
	return &jsonschema.Schema{Ref: "#/components/schemas/" + name}
}

// Build assembles the document of s. Every error names what is wrong and wraps
// ErrInvalid; all problems are joined.
func Build(s Spec) (*Document, error) {
	var errs []error
	nameRE := componentName()
	for name := range s.Schemas {
		if !nameRE.MatchString(name) {
			errs = append(errs, fmt.Errorf("%w: component name %q", ErrInvalid, name))
		}
	}
	has := func(name string) bool { _, ok := s.Schemas[name]; return ok }
	if s.ProblemSchema == "" || !has(s.ProblemSchema) {
		errs = append(errs, fmt.Errorf("%w: problem schema %q is not a component", ErrInvalid, s.ProblemSchema))
	}
	doc := &Document{
		OpenAPI: Version,
		Info:    Info{Title: s.Title, Version: s.Version},
		Paths:   map[string]PathItem{},
		Components: Components{
			Schemas: map[string]*jsonschema.Schema{},
			SecuritySchemes: map[string]SecurityScheme{
				"sessionCookie": {Type: "apiKey", In: "cookie", Name: s.CookieName,
					Description: "browser session cookie"},
				"bearer": {Type: "http", Scheme: "bearer",
					Description: "session token or API key"},
			},
		},
		Security: defaultSecurity(),
	}
	maps.Copy(doc.Components.Schemas, s.Schemas)
	seenID := map[string]bool{}
	ops := slices.Clone(s.Operations)
	slices.SortFunc(ops, func(a, b Operation) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
	for _, op := range ops {
		method := strings.ToLower(op.Method)
		if op.OperationID == "" || seenID[op.OperationID] {
			errs = append(errs, fmt.Errorf("%w: operationId %q empty or duplicated", ErrInvalid, op.OperationID))
		}
		seenID[op.OperationID] = true
		item := doc.Paths[op.Path]
		if item == nil {
			item = PathItem{}
			doc.Paths[op.Path] = item
		}
		if _, dup := item[method]; dup {
			errs = append(errs, fmt.Errorf("%w: %s %s described twice", ErrInvalid, op.Method, op.Path))
			continue
		}
		obj, err := operationObject(op, s.ProblemSchema, has)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		item[method] = obj
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return doc, nil
}

func defaultSecurity() []map[string][]string {
	return []map[string][]string{{"sessionCookie": {}}, {"bearer": {}}}
}

func operationObject(op Operation, problem string, has func(string) bool) (*OperationObject, error) {
	obj := &OperationObject{
		OperationID: op.OperationID,
		Summary:     op.Summary,
		Description: op.Description,
		Tags:        op.Tags,
		Permission:  op.Permission,
		Guards:      op.Guards,
		Responses: map[string]Response{
			"default": {Description: "Problem", Content: map[string]MediaType{
				"application/problem+json": {Schema: Ref(problem)}}},
		},
	}
	if op.Public {
		empty := []map[string][]string{}
		obj.Security = &empty
	}
	for _, p := range op.Parameters {
		obj.Parameters = append(obj.Parameters, ParameterObject{Name: p.Name, In: p.In,
			Required: p.Required || p.In == "path", Schema: p.Schema})
	}
	if op.RequestBody != "" {
		if !has(op.RequestBody) {
			return nil, fmt.Errorf("%w: %s: request body %q is not a component", ErrInvalid, op.OperationID, op.RequestBody)
		}
		obj.RequestBody = &RequestBodyObject{Required: true, Content: map[string]MediaType{
			"application/json": {Schema: Ref(op.RequestBody)}}}
	}
	status := op.SuccessStatus
	if status == 0 {
		status = 200
	}
	resp := Response{Description: successText(status)}
	if op.Success != "" {
		if !has(op.Success) {
			return nil, fmt.Errorf("%w: %s: success body %q is not a component", ErrInvalid, op.OperationID, op.Success)
		}
		resp.Content = map[string]MediaType{"application/json": {Schema: Ref(op.Success)}}
	}
	obj.Responses[fmt.Sprint(status)] = resp
	return obj, nil
}

func successText(status int) string {
	switch status {
	case 201:
		return "Created"
	case 204:
		return "No Content"
	default:
		return "OK"
	}
}

// JSON returns the document as indented JSON with a trailing newline: the
// committed form of core/openapi/core.json.
func (d *Document) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("openapi: encode: %w", err)
	}
	return buf.Bytes(), nil
}
