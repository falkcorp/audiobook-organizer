// file: core/httpapi/openapi.go
// version: 1.0.1
// guid: 586f0310-28e5-4cc9-9e26-2a4a520b7395
// last-edited: 2026-11-07

package httpapi

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/falkcorp/mediacore/internal/jsonschemax"
	"github.com/falkcorp/mediacore/internal/openapi"
	"github.com/falkcorp/mediacore/rbac"
)

// problemComponent is the component name of Problem in every document.
const problemComponent = "core.Problem"

// OpenAPI builds the OpenAPI 3.1 document of one owner ("core" or a module
// ID) from its native routes (05-http-api §7). Info.Title is the owner and
// Info.Version is Options.OpenAPI.Version; the kernel sets both per owner.
// Two Go types that map to one component name are ErrSchema.
func (r *Router) OpenAPI(owner string) (*openapi.Document, error) {
	problem, err := jsonschemax.For[Problem](schemaOverrides())
	if err != nil {
		return nil, fmt.Errorf("%w: Problem: %v", ErrSchema, err)
	}
	schemas := map[string]*jsonschema.Schema{problemComponent: problem}
	types := map[string]reflect.Type{problemComponent: reflect.TypeFor[Problem]()}
	var errs []error
	addSchema := func(name string, t reflect.Type, s *jsonschema.Schema) {
		if prev, ok := types[name]; ok && prev != t {
			errs = append(errs, fmt.Errorf("%w: %s and %s both name component %q", ErrSchema, prev, t, name))
			return
		}
		types[name], schemas[name] = t, s
	}
	var ops []openapi.Operation
	for _, e := range r.routeSnapshot() {
		if e.info.Owner != owner || e.info.Kind != RouteKindNative || len(e.errs) > 0 {
			continue
		}
		op := openapi.Operation{
			Method:        e.info.Method,
			Path:          openAPIPath(e.info.Pattern),
			OperationID:   owner + "_" + e.info.Name,
			Summary:       e.route.Summary,
			Description:   e.route.Doc,
			Tags:          e.route.Tags,
			Permission:    e.info.Perm.String(),
			Public:        e.info.Perm.Kind() == rbac.KindPublic,
			Guards:        guardNames(e.info.Guards),
			SuccessStatus: e.status,
		}
		for _, p := range e.binding.parameters() {
			op.Parameters = append(op.Parameters, openapi.Parameter{Name: p.name, In: p.in, Schema: p.schema})
		}
		if e.bodyName != "" {
			addSchema(e.bodyName, e.bodyType, e.bodySch)
			op.RequestBody = e.bodyName
		}
		if e.respName != "" {
			addSchema(e.respName, e.respType, e.respSch)
			op.Success = e.respName
		}
		ops = append(ops, op)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	doc, err := openapi.Build(openapi.Spec{
		Title: owner, Version: r.opts.OpenAPI.Version, CookieName: r.opts.OpenAPI.CookieName,
		ProblemSchema: problemComponent, Schemas: schemas, Operations: ops,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSchema, err)
	}
	return doc, nil
}

// openAPIPath turns a ServeMux pattern path into an OpenAPI path template:
// "{name...}" becomes "{name}" and a trailing "{$}" is dropped.
func openAPIPath(p string) string {
	p = strings.TrimSuffix(p, "{$}")
	return strings.ReplaceAll(p, "...}", "}")
}

func guardNames(g Guards) []string {
	var out []string
	if g.Interactive {
		out = append(out, "interactive")
	}
	if g.Owner {
		out = append(out, "owner")
	}
	return out
}
