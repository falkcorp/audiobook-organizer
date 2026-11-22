// file: core/internal/executor/module_client.go
// version: 1.0.0
// guid: 7b3e1a95-0c64-4d28-9f1e-52a8d6c4e703
// last-edited: 2026-11-22

package executor

import (
	"context"
	"fmt"
	"iter"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// moduleKey is the context key of a module client's call. Its type is
// unexported, so only ModuleClient can put a module's name where principal
// reads it: a caller cannot hand the executor a system actor through a
// context.
type moduleKey struct{}

// moduleChecker is the checker of a module's system actor (systemChecker),
// marked with the module so mayView can tell a module's own kinds.
type moduleChecker struct {
	rbac.Checker
	module string
}

// principal is the caller of a Client call: the request's principal
// (Options.Principal) when ctx carries one, else, for a call made through a
// ModuleClient, the module's system actor with its checker (SEC2-15), else
// none (a kernel-internal call, which the executor does not permission-check).
// The request's principal wins: a module that serves a request acts as the
// user it serves.
func (e *executor) principal(ctx context.Context) (tasks.Actor, rbac.Checker, bool) {
	if e.opts.Principal != nil {
		if a, who, ok := e.opts.Principal(ctx); ok {
			return a, who, true
		}
	}
	if m, ok := ctx.Value(moduleKey{}).(string); ok && m != "" {
		return tasks.Actor{AuthMethod: systemMethod, System: m}, moduleChecker{Checker: e.systemChecker(m), module: m}, true
	}
	return tasks.Actor{}, nil, false
}

// ModuleClient is module's tasks.Client over x (module.Deps.Tasks, 05-tasks
// 5T.3.10). A call made inside a request acts as that request's principal. A
// call with no request in its context acts as the module's system actor:
// Start stamps it over whatever req.Actor the caller set, so a module cannot
// start a run as another actor, and every method applies the route's own
// permission rule to that actor. The actor may start the module's own kinds
// and the kinds its manifest lists in TaskStarts, holds no core:admin and no
// AuthRequired scope, and reads the runs of its own kinds.
//
// x must be an executor this package built.
func ModuleClient(x tasks.Executor, module string) (tasks.Client, error) {
	e, ok := x.(*executor)
	if !ok {
		return nil, fmt.Errorf("executor: ModuleClient over %T, not an executor built by New", x)
	}
	if module == "" {
		return nil, fmt.Errorf("executor: ModuleClient needs a module")
	}
	return moduleClient{e: e, module: module}, nil
}

// moduleClient is tasks.Client with the module in every call's context.
type moduleClient struct {
	e      *executor
	module string
}

var _ tasks.Client = moduleClient{}

func (m moduleClient) ctx(ctx context.Context) context.Context {
	return context.WithValue(ctx, moduleKey{}, m.module)
}

func (m moduleClient) Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error) {
	// The actor is never the caller's to name: with no principal in ctx the
	// executor acts as req.Actor, so it is set here, and with one it is
	// overwritten by the stamp.
	req.Actor = tasks.Actor{AuthMethod: systemMethod, System: m.module}
	return m.e.Start(m.ctx(ctx), kind, req)
}

func (m moduleClient) Get(ctx context.Context, id tasks.RunID) (tasks.RunView, error) {
	return m.e.Get(m.ctx(ctx), id)
}

func (m moduleClient) Runs(ctx context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunView, error] {
	return m.e.Runs(m.ctx(ctx), q)
}

func (m moduleClient) Cancel(ctx context.Context, id tasks.RunID) error {
	return m.e.Cancel(m.ctx(ctx), id)
}

func (m moduleClient) Decide(ctx context.Context, id tasks.RunID, d tasks.Decision) error {
	return m.e.Decide(m.ctx(ctx), id, d)
}

func (m moduleClient) Pause(ctx context.Context, reason string) error {
	return m.e.Pause(m.ctx(ctx), reason)
}

func (m moduleClient) Unpause(ctx context.Context) error { return m.e.Unpause(m.ctx(ctx)) }

func (m moduleClient) PauseState(ctx context.Context) (tasks.Pause, bool, error) {
	return m.e.PauseState(m.ctx(ctx))
}

func (m moduleClient) Timeline(ctx context.Context, q tasks.TimelineQuery) ([]tasks.RunView, error) {
	return m.e.Timeline(m.ctx(ctx), q)
}

func (m moduleClient) Log(ctx context.Context, id tasks.RunID, q tasks.PageQuery) iter.Seq2[tasks.LogLine, error] {
	return m.e.Log(m.ctx(ctx), id, q)
}

func (m moduleClient) Discard(ctx context.Context, id tasks.RunID) error {
	return m.e.Discard(m.ctx(ctx), id)
}
