// file: core/httpapi/openapi_test.go
// version: 1.0.0
// guid: 62fd4119-7a30-4f91-9d79-ec8d3d43bc9c
// last-edited: 2026-11-07

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/falkcorp/mediacore/internal/openapi/openapitest"
	"github.com/falkcorp/mediacore/rbac"
)

func TestRouterOpenAPI_IsValid31(t *testing.T) {
	r, g := newRouter(t, "getGadget", "createGadget", "listGadgets")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public,
		Summary: "One gadget", Tags: []string{"gadgets"}}, getGadget)
	Handle(g, Route{Method: http.MethodPost, Path: "/gadgets", Name: "createGadget", Perm: rbac.Public},
		func(_ context.Context, in *In[createGadgetReq]) (Created[Gadget], error) {
			return Created[Gadget]{Body: in.Req.Body}, nil
		})
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets", Name: "listGadgets", Perm: rbac.Public},
		func(context.Context, *In[listGadgetsReq]) (Page[Gadget], error) { return Page[Gadget]{}, nil })
	mustBuild(t, r)

	doc, err := r.OpenAPI("batcave")
	if err != nil {
		t.Fatal(err)
	}
	b, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := openapitest.Validate(b); err != nil {
		t.Fatalf("document is not valid OpenAPI 3.1: %v\n%s", err, b)
	}
	var d struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"core.Problem", "batcave.Gadget", "batcave.Page_Gadget"} {
		if _, ok := d.Components.Schemas[name]; !ok {
			t.Errorf("component %s missing; have %v", name, d.Components.Schemas)
		}
	}
	if _, ok := d.Paths["/api/batcave/v1/gadgets/{id}"]["get"]; !ok {
		t.Errorf("path /api/batcave/v1/gadgets/{id} get missing: %v", d.Paths)
	}
}

func TestRouterOpenAPI_ComponentCollision(t *testing.T) {
	type Gadget struct { // same component name as the package-level Gadget, another Go type
		Weight int `json:"weight"`
	}
	r, g := newRouter(t, "getGadget", "weighGadget")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets/{id}", Name: "getGadget", Perm: rbac.Public}, getGadget)
	Handle(g, Route{Method: http.MethodGet, Path: "/scales/{id}", Name: "weighGadget", Perm: rbac.Public},
		func(context.Context, *In[getGadgetReq]) (Gadget, error) { return Gadget{}, nil })
	mustBuild(t, r)
	if _, err := r.OpenAPI("batcave"); !errors.Is(err, ErrSchema) {
		t.Fatalf("OpenAPI = %v, want ErrSchema for two types named batcave.Gadget", err)
	}
}
