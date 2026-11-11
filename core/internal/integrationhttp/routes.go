// file: core/internal/integrationhttp/routes.go
// version: 1.0.0
// guid: 91c5d7e2-3a84-4b1f-b6d0-58e2a4c7f193
// last-edited: 2026-11-11

// Package integrationhttp is the HTTP surface of core's integration registry
// (05-metadata M1.3 "HTTP surface", F7-10): the adapters list, the
// {kind}/instances routes, the unsaved-body and per-instance test routes, an
// instance's health, and DELETE .../hold, registered on httpapi's core group
// (/api/core/v1). Each route is a thin adapter over integrations.Registry,
// which owns every security rule: origin binding, the clearing of secrets on
// a destination change, ProtectAdmin for Local, locked fields, a Test that
// returns no body. This package passes a request through unchanged so those
// rules apply and adds no reach of its own: it never resolves a secrets.Ref,
// never fills a secret into a body, builds the Registry's secretsIn map from
// the request body only, and never retries a refused Put or Test with stored
// values.
//
// The package is dark until F7-09 mounts it: nothing imports it before then.
//
// Layer L6 (03 R4): internal/<capability>http, imported only by kernel. It
// holds no package-level mutable state (R5).
package integrationhttp

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/rbac"
)

// The two permissions the routes declare (05-rbac §7.1: core.integration.*).
// manage implies read in the catalog.
const (
	permRead   rbac.Permission = "core:integrations.read"
	permManage rbac.Permission = "core:integrations.manage"
)

// maxBody bounds a body: an instance's settings are a handful of fields, and
// a secret is capped at maxSecretBytes by validation, so a request has no
// need for httpapi's 1 MiB default.
const maxBody = 64 << 10

// api is what the handlers share. Its fields are fixed at Register.
type api struct {
	reg   *integrations.Registry
	holds HoldReleaser
	audit audit.Logger
	base  string // the group's base path, for Location and the audit route
}

// Register registers the integration routes on g, which must be the router's
// CoreGroup(). reg serves every route; holds releases a hold for the hold
// route (providerhttp's *Registry at F7-09; nil makes that route answer 503);
// auditLog records core.integration.test, the one action in 05-rbac §7.1 the
// Registry does not record itself (a nil auditLog makes the test routes
// answer 503: a probe that sends a credential is never unaudited). Create,
// update, change_url, write_secret and delete are the Registry's, recorded
// in the batch that commits the change.
func Register(g *httpapi.Group, reg *integrations.Registry, holds HoldReleaser, auditLog audit.Logger) {
	a := &api{reg: reg, holds: holds, audit: auditLog, base: g.Base()}
	tags := []string{"integrations"}
	read := rbac.Need(permRead)
	manage := rbac.Need(permManage)

	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/integrations/adapters", Name: "listIntegrationAdapters", Perm: read, Tags: tags,
		Summary: "List the adapters this binary has, with each one's settings schema",
		Doc:     "Optional ?kind= narrows the list to one kind. The schema marks secret fields x-mediacore-secret.",
	}, a.listAdapters)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/integrations/{kind}/instances", Name: "listIntegrationInstances", Perm: read, Tags: tags,
		Summary: "List a kind's instances",
		Doc:     "Secret fields are never returned; secrets_set says which hold a value.",
	}, a.listInstances)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodPost, Path: "/integrations/{kind}/instances", Name: "createIntegrationInstance", Perm: manage, Tags: tags,
		MaxBody: maxBody, Summary: "Create an instance",
		Doc: "Secret values go in secrets and are write-only. A body naming an existing instance is 409.",
	}, a.createInstance)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/integrations/{kind}/instances/{id}", Name: "getIntegrationInstance", Perm: read, Tags: tags,
		Summary: "Read an instance",
	}, a.getInstance)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodPut, Path: "/integrations/{kind}/instances/{id}", Name: "updateIntegrationInstance", Perm: manage, Tags: tags,
		MaxBody: maxBody, Summary: "Replace an instance's editable fields",
		Doc: "The body carries the stored version. A secret the body omits keeps its stored value while no destination moves; settings.<field> = null clears it.",
	}, a.updateInstance)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodDelete, Path: "/integrations/{kind}/instances/{id}", Name: "deleteIntegrationInstance", Perm: manage, Tags: tags,
		Summary: "Delete an instance and its secrets",
	}, a.deleteInstance)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodPost, Path: "/integrations/{kind}/test", Name: "testIntegration", Perm: manage, Tags: tags,
		MaxBody: maxBody, RateClass: httpapi.RateExpensive, Summary: "Test an unsaved configuration",
		Doc: "No stored instance is involved: only the body's secrets are sent. The answer is ok, class and status, never remote text.",
	}, a.testNew)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodPost, Path: "/integrations/{kind}/instances/{id}/test", Name: "testIntegrationInstance", Perm: manage, Tags: tags,
		MaxBody: maxBody, RateClass: httpapi.RateExpensive, Summary: "Test an unsaved body for a stored instance",
		Doc: "A secret the body omits is filled from the stored one only while no destination origin changes.",
	}, a.testInstance)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodGet, Path: "/integrations/{kind}/instances/{id}/health", Name: "getIntegrationInstanceHealth", Perm: read, Tags: tags,
		RateClass: httpapi.RateExpensive, Summary: "Probe an instance and report its health",
	}, a.getHealth)
	httpapi.Handle(g, httpapi.Route{
		Method: http.MethodDelete, Path: "/integrations/{kind}/instances/{id}/hold", Name: "releaseIntegrationInstanceHold", Perm: manage, Tags: tags,
		Summary: "Release the instance's active hold",
		Doc:     "204 whether or not a hold was active.",
	}, a.releaseHold)
}

// ---- requests -------------------------------------------------------------

type listAdaptersReq struct {
	Kind string `query:"kind"`
	httpapi.PageParams
}

// Validate refuses a ?kind= that is not a kind.
func (r *listAdaptersReq) Validate() error {
	if r.Kind != "" && !integrations.Kind(r.Kind).Valid() {
		return httpapi.Invalid(httpapi.Field("/query/kind", "invalid_enum", "must be one of ai, download, indexer, metadata, notify"))
	}
	return nil
}

type listInstancesReq struct {
	Kind string `path:"kind"`
	httpapi.PageParams
}

type instanceReq struct {
	Kind string `path:"kind"`
	ID   string `path:"id"`
}

type createReq struct {
	Kind string                  `path:"kind"`
	Body IntegrationInstanceBody `body:"json"`
}

// Validate checks the body for a create.
func (r *createReq) Validate() error { return invalid(r.Body.validate(modeCreate, "")) }

type updateReq struct {
	Kind string                  `path:"kind"`
	ID   string                  `path:"id"`
	Body IntegrationInstanceBody `body:"json"`
}

// Validate checks the body for a replace.
func (r *updateReq) Validate() error { return invalid(r.Body.validate(modeReplace, r.ID)) }

type testNewReq struct {
	Kind string                  `path:"kind"`
	Body IntegrationInstanceBody `body:"json"`
}

// Validate checks the body for the unscoped test.
func (r *testNewReq) Validate() error { return invalid(r.Body.validate(modeTest, "")) }

type testInstanceReq struct {
	Kind string                  `path:"kind"`
	ID   string                  `path:"id"`
	Body IntegrationInstanceBody `body:"json"`
}

// Validate checks the body for the instance-scoped test.
func (r *testInstanceReq) Validate() error { return invalid(r.Body.validate(modeTest, r.ID)) }

// invalid is errs as one 422, or nil for none.
func invalid(errs []httpapi.FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	return httpapi.Invalid(errs...)
}

// ---- path binding ---------------------------------------------------------

// errNoKind is the 404 of a path kind that is not one. The path value is not
// echoed.
func errNoKind() error {
	return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such integration kind"}
}

// errNoInstance is the 404 of a path ID that names no possible instance.
func errNoInstance() error {
	return &httpapi.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "no such integration instance"}
}

// kindRef is the path's kind: 404 when it is not one.
func kindRef(kind string) (integrations.Kind, error) {
	k := integrations.Kind(kind)
	if !k.Valid() {
		return "", errNoKind()
	}
	return k, nil
}

// instanceRef is the path's {kind}/{id}: 404 for a kind that is not one and
// for an ID outside the grammar (no instance has it).
func instanceRef(kind, id string) (integrations.Ref, error) {
	k, err := kindRef(kind)
	if err != nil {
		return integrations.Ref{}, err
	}
	ref := integrations.Ref{Kind: k, Instance: integrations.InstanceID(id)}
	if ref.Instance.Validate() != nil {
		return integrations.Ref{}, errNoInstance()
	}
	return ref, nil
}

// metasOf is kind's adapters by ID, for the secret fields of its instances.
func (a *api) metasOf(kind integrations.Kind) map[integrations.AdapterID]integrations.Meta {
	out := map[integrations.AdapterID]integrations.Meta{}
	for m := range a.reg.Adapters(kind) {
		out[m.ID] = m
	}
	return out
}

// view renders cfg, with its adapter's secret fields when this binary has it.
func view(cfg integrations.InstanceConfig, metas map[integrations.AdapterID]integrations.Meta) IntegrationInstance {
	var fields map[string]bool
	if m, ok := metas[cfg.Adapter]; ok {
		fields = secretFieldsOf(m)
	}
	return instanceView(cfg, fields)
}

// page is items[offset:offset+limit] as httpapi's default list shape.
func page[T any](items []T, p httpapi.PageParams) httpapi.Page[T] {
	total := len(items)
	lo := min(max(p.Offset, 0), total)
	hi := min(lo+p.Limit, total)
	out := slices.Clone(items[lo:hi])
	if out == nil {
		out = []T{}
	}
	return httpapi.Page[T]{Items: out, Count: len(out), Limit: p.Limit, Offset: p.Offset, Total: int64(total)}
}

// ---- handlers -------------------------------------------------------------

func (a *api) listAdapters(_ context.Context, in *httpapi.In[listAdaptersReq]) (httpapi.Page[IntegrationAdapter], error) {
	if err := a.guard(in.Principal); err != nil {
		return httpapi.Page[IntegrationAdapter]{}, err
	}
	kinds := integrations.Kinds()
	if in.Req.Kind != "" {
		kinds = []integrations.Kind{integrations.Kind(in.Req.Kind)}
	}
	var all []IntegrationAdapter
	for _, k := range kinds {
		for m := range a.reg.Adapters(k) {
			all = append(all, adapterView(m))
		}
	}
	return page(all, in.Req.PageParams), nil
}

func (a *api) listInstances(_ context.Context, in *httpapi.In[listInstancesReq]) (httpapi.Page[IntegrationInstance], error) {
	if err := a.guard(in.Principal); err != nil {
		return httpapi.Page[IntegrationInstance]{}, err
	}
	kind, err := kindRef(in.Req.Kind)
	if err != nil {
		return httpapi.Page[IntegrationInstance]{}, err
	}
	metas := a.metasOf(kind)
	var all []IntegrationInstance
	for cfg := range a.reg.Instances(kind) {
		all = append(all, view(cfg, metas))
	}
	return page(all, in.Req.PageParams), nil
}

func (a *api) getInstance(_ context.Context, in *httpapi.In[instanceReq]) (IntegrationInstance, error) {
	if err := a.guard(in.Principal); err != nil {
		return IntegrationInstance{}, err
	}
	ref, err := instanceRef(in.Req.Kind, in.Req.ID)
	if err != nil {
		return IntegrationInstance{}, err
	}
	cfg, ok := a.reg.Config(ref)
	if !ok {
		return IntegrationInstance{}, errNoInstance()
	}
	return view(cfg, a.metasOf(ref.Kind)), nil
}

func (a *api) createInstance(ctx context.Context, in *httpapi.In[createReq]) (httpapi.Created[IntegrationInstance], error) {
	var zero httpapi.Created[IntegrationInstance]
	if err := a.guard(in.Principal); err != nil {
		return zero, err
	}
	kind, err := kindRef(in.Req.Kind)
	if err != nil {
		return zero, err
	}
	ref := integrations.Ref{Kind: kind, Instance: integrations.InstanceID(in.Req.Body.ID)}
	inst, secretsIn, err := in.Req.Body.config(ref, 0)
	if err != nil {
		return zero, err
	}
	cfg, err := a.reg.Put(ctx, inst, secretsIn)
	if err != nil {
		return zero, mapError(err)
	}
	loc := a.base + "/integrations/" + url.PathEscape(string(kind)) + "/instances/" + url.PathEscape(string(ref.Instance))
	return httpapi.Created[IntegrationInstance]{Location: loc, Body: view(cfg, a.metasOf(kind))}, nil
}

func (a *api) updateInstance(ctx context.Context, in *httpapi.In[updateReq]) (IntegrationInstance, error) {
	if err := a.guard(in.Principal); err != nil {
		return IntegrationInstance{}, err
	}
	ref, err := instanceRef(in.Req.Kind, in.Req.ID)
	if err != nil {
		return IntegrationInstance{}, err
	}
	// A replace never creates: the body's version is at least 1 (Validate),
	// which no absent row can match, but 404 says what is wrong with a replace of
	// an instance that does not exist.
	if _, ok := a.reg.Config(ref); !ok {
		return IntegrationInstance{}, errNoInstance()
	}
	inst, secretsIn, err := in.Req.Body.config(ref, in.Req.Body.Version)
	if err != nil {
		return IntegrationInstance{}, err
	}
	cfg, err := a.reg.Put(ctx, inst, secretsIn)
	if err != nil {
		return IntegrationInstance{}, mapError(err)
	}
	return view(cfg, a.metasOf(ref.Kind)), nil
}

func (a *api) deleteInstance(ctx context.Context, in *httpapi.In[instanceReq]) (httpapi.NoContent, error) {
	if err := a.guard(in.Principal); err != nil {
		return httpapi.NoContent{}, err
	}
	ref, err := instanceRef(in.Req.Kind, in.Req.ID)
	if err != nil {
		return httpapi.NoContent{}, err
	}
	if err := a.reg.Delete(ctx, ref); err != nil {
		return httpapi.NoContent{}, mapError(err)
	}
	return httpapi.NoContent{}, nil
}

func (a *api) getHealth(ctx context.Context, in *httpapi.In[instanceReq]) (IntegrationHealth, error) {
	if err := a.guard(in.Principal); err != nil {
		return IntegrationHealth{}, err
	}
	ref, err := instanceRef(in.Req.Kind, in.Req.ID)
	if err != nil {
		return IntegrationHealth{}, err
	}
	h, err := a.reg.Health(ctx, ref)
	if err != nil {
		return IntegrationHealth{}, mapError(err)
	}
	return healthView(h), nil
}

func (a *api) testNew(ctx context.Context, in *httpapi.In[testNewReq]) (IntegrationTestResult, error) {
	if err := a.guard(in.Principal); err != nil {
		return IntegrationTestResult{}, err
	}
	kind, err := kindRef(in.Req.Kind)
	if err != nil {
		return IntegrationTestResult{}, err
	}
	// The unscoped form: an empty instance ID names no stored instance, so
	// the Registry can use no stored secret (M1.3 Test).
	return a.runTest(ctx, integrations.Ref{Kind: kind}, &in.Req.Body, "POST "+a.base+"/integrations/{kind}/test")
}

func (a *api) testInstance(ctx context.Context, in *httpapi.In[testInstanceReq]) (IntegrationTestResult, error) {
	if err := a.guard(in.Principal); err != nil {
		return IntegrationTestResult{}, err
	}
	ref, err := instanceRef(in.Req.Kind, in.Req.ID)
	if err != nil {
		return IntegrationTestResult{}, err
	}
	// A test of an instance that does not exist would silently become the
	// unscoped form; it is 404 instead.
	if _, ok := a.reg.Config(ref); !ok {
		return IntegrationTestResult{}, errNoInstance()
	}
	return a.runTest(ctx, ref, &in.Req.Body, "POST "+a.base+"/integrations/{kind}/instances/{id}/test")
}

// runTest hands the body and ref to Registry.Test unchanged and records
// core.integration.test. The record follows the test (it carries the
// result) and is the route's own because the Registry audits only its
// writes. A failed record answers 503 and withholds the result: a test whose
// outcome cannot be recorded tells its caller nothing. Registry refusals
// (403, 409, 422) ran no test and record nothing.
func (a *api) runTest(ctx context.Context, ref integrations.Ref, body *IntegrationInstanceBody, route string) (IntegrationTestResult, error) {
	if a.audit == nil {
		return IntegrationTestResult{}, httpapi.Unavailable("tests cannot be audited right now", 5*time.Second)
	}
	inst, secretsIn, err := body.config(ref, 0)
	if err != nil {
		return IntegrationTestResult{}, err
	}
	res, err := a.reg.Test(ctx, inst, secretsIn)
	if err != nil {
		return IntegrationTestResult{}, mapError(err)
	}
	ev := audit.Event{
		Action: "core.integration.test", Outcome: audit.Success,
		Target: entity.Ref{Module: "core", Kind: "integration", ID: ref.String()}, Route: route,
		Details: map[string]any{
			"instance": ref.String(), "adapter": body.Adapter, "scoped": ref.Instance != "",
			"ok": res.OK, "class": res.Class.String(), "status": res.Status,
		},
	}
	if err := a.audit.Record(ctx, ev); err != nil {
		return IntegrationTestResult{}, httpapi.Unavailable("the test could not be recorded", 5*time.Second)
	}
	return IntegrationTestResult{OK: res.OK, Class: res.Class.String(), Status: res.Status}, nil
}
