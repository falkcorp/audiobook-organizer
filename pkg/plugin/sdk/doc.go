// file: pkg/plugin/sdk/doc.go
// version: 2.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-05-08

// Package sdk provides the STABLE public API for audiobook-organizer plugins.
//
// Plugin authors should import only this package (plus stdlib and log/slog).
// The internal backplane (internal/operations/registry, internal/auth) is the
// implementation detail; this package exposes type aliases and constants that
// form the stable contract. Breaking changes to the types below will increment
// the major version.
//
// # Minimal plugin example
//
// The following 30-line example is a complete, compilable plugin.
// It registers one operation that logs a greeting and exits.
//
//	package greeter
//
//	import (
//	    "context"
//	    "encoding/json"
//	    "log/slog"
//	    "time"
//
//	    "github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
//	)
//
//	type Plugin struct{}
//
//	func New() *Plugin { return &Plugin{} }
//
//	func (p *Plugin) ID()      string { return "greeter" }
//	func (p *Plugin) Name()    string { return "Greeter" }
//	func (p *Plugin) Version() string { return "1.0.0" }
//
//	func (p *Plugin) Register(r sdk.Registry) error {
//	    return r.RegisterOp(sdk.OperationDef{
//	        ID:              "greeter.hello",
//	        Plugin:          "greeter",
//	        DisplayName:     "Say hello",
//	        Description:     "Logs a greeting and exits.",
//	        ResumePolicy:    sdk.ResumeDrop,
//	        DefaultPriority: sdk.PriorityLow,
//	        ConcurrencyKey:  "greeter.hello",
//	        Cancellable:     false,
//	        Isolate:         false,
//	        Timeout:         30 * time.Second,
//	        Capabilities:    nil,
//	        // Every def must declare how it keeps the watchdog informed;
//	        // RegisterOp rejects the zero value. This one does no reportable
//	        // work, so it declares LivenessNone and names its own budget.
//	        // Prefer LivenessRunItems (registry.RunItems stamps per item) or
//	        // LivenessManual (call rep.UpdateProgress yourself) for real work.
//	        // Note rep.Log does NOT count as progress: a spin loop that logs
//	        // is still a spin loop.
//	        Liveness:        sdk.LivenessNone,
//	        ProgressTimeout: 30 * time.Second,
//	        Run: func(_ context.Context, _ json.RawMessage, rep sdk.Reporter) error {
//	            return rep.Log(slog.LevelInfo, "hello from greeter plugin")
//	        },
//	    })
//	}
//
// # Stability contract
//
// This package is STABLE as of UOS-15. The following identifiers will not be
// removed or have their signatures changed in a backwards-incompatible way
// without a major-version bump:
//
//   - Plugin interface (ID, Name, Version, Register)
//   - Registry interface (RegisterOp, EnqueueOp)
//   - OperationDef struct and all exported fields
//   - Reporter interface (UpdateProgress, Log, Logger, Checkpoint, IsCanceled, RunPhase, Trigger)
//   - ResumePolicy constants (ResumeRestart, ResumeRequeue, ResumeDrop, ResumeAsk)
//   - Priority constants (PriorityLow, PriorityNormal, PriorityHigh)
//   - ActorMode constants (ActorContext, ActorSystem)
//   - Capability constants (all Cap* identifiers)
//   - EnqueueOption type and option constructors (WithParent, WithActor, WithPriority)
//   - Error variables (ErrCanceled, ErrQuiesced, ErrPluginCapabilityMissing)
//
// # Further reading
//
// See docs/development/writing-a-plugin.md for the full tutorial covering
// lifecycle, ResumePolicy decision tree, isolation, capability declarations,
// schedules/triggers, and testing patterns.
package sdk
