<!-- file: docs/development/writing-a-plugin.md -->
<!-- version: 1.0.1 -->
<!-- guid: f7a8b9c0-d1e2-3456-g789-h0123456789ab -->
<!-- last-edited: 2026-08-22 -->

# Writing a Plugin for Audiobook Organizer

This guide explains how to register background operations using the
[`pkg/plugin/sdk`](../../pkg/plugin/sdk/) package — the stable public API for
plugin authors.

## Contents

1. [Core concepts](#1-core-concepts)
2. [Lifecycle: Plugin.Register → Registry.RegisterOp](#2-lifecycle)
3. [Picking a ResumePolicy](#3-resumepolicy-decision-tree)
4. [When to set Isolate: true](#4-isolation)
5. [Capability declarations](#5-capability-declarations)
6. [Schedules, triggers, and ad-hoc invocation](#6-schedules-triggers-and-ad-hoc-invocation)
7. [Testing your plugin](#7-testing-your-plugin)
8. [Worked example: import-counter plugin](#8-worked-example)

---

## 1. Core Concepts

**Plugin** — a struct that satisfies `sdk.Plugin`. It holds any dependencies the
operation needs (database handles, API clients, etc.) and calls
`Registry.RegisterOp` during startup.

**OperationDef** — the static description of one unit of async work. It declares
an ID, priority, resume semantics, capabilities, and a `Run` function. Every
field is set once at registration; nothing mutates it afterward.

**Registry** — the narrow interface given to `Plugin.Register`. It exposes two
methods:

- `RegisterOp(def OperationDef) error` — registers the def; call during startup.
- `EnqueueOp(ctx, defID, params, opts...) (runID string, err error)` — enqueues
  a new run of a previously-registered op.

**Reporter** — the per-run handle passed to `Run`. Use it to emit progress,
structured logs, and checkpoints.

---

## 2. Lifecycle

The startup sequence is:

```
server.New()
  └─ pluginRegistry.Register(myPlugin)
       └─ myPlugin.Register(r Registry)
            └─ r.RegisterOp(def1)
            └─ r.RegisterOp(def2)
            ...
```

`Register` is called once, synchronously, before any requests are served.
It must not start goroutines, block on I/O, or call `EnqueueOp` — it only
registers definitions.

Typical plugin skeleton:

```go
type Plugin struct {
    store database.Store
    // ... other deps
}

func New(store database.Store) *Plugin { return &Plugin{store: store} }

func (p *Plugin) ID()      string { return "myplug" }
func (p *Plugin) Name()    string { return "My Plugin" }
func (p *Plugin) Version() string { return "1.0.0" }

func (p *Plugin) Register(r sdk.Registry) error {
    defs := []sdk.OperationDef{
        p.scanDef(),
        p.cleanupDef(),
    }
    for _, d := range defs {
        if err := r.RegisterOp(d); err != nil {
            return fmt.Errorf("register %s: %w", d.ID, err)
        }
    }
    return nil
}
```

The `ID()` value is used as the `Plugin` field in every `OperationDef`. Keep
IDs lowercase, slug-style (e.g., `"acoustid"`, `"itunes"`). IDs must be globally
unique across all plugins; collisions cause `RegisterOp` to return an error.

---

## 3. ResumePolicy Decision Tree

`ResumePolicy` controls what happens when the server restarts with an in-flight
run. It is **required** — `RegisterOp` rejects `ResumeUnspecified`.

```
Is the operation idempotent?
  ├─ Yes → Can it safely re-run from zero every restart?
  │         ├─ Yes → ResumeRequeue   (re-enqueue as a fresh run)
  │         └─ No  → ResumeRestart  (reload last checkpoint, call Run again)
  └─ No → Is partial work harmful if abandoned?
            ├─ Yes → ResumeAsk      (surface in UI, wait for user)
            └─ No  → ResumeDrop    (abandon and mark interrupted_dropped)
```

| Policy         | Use when                                                     |
|----------------|--------------------------------------------------------------|
| `ResumeRequeue`  | Full re-scan is cheap; idempotent writes only              |
| `ResumeRestart`  | Operation checkpoints progress; can resume mid-way         |
| `ResumeDrop`     | Short maintenance tasks; losing a partial run is fine      |
| `ResumeAsk`      | Irreversible actions where partial completion needs review |

**Real-world examples from this codebase:**

- `dedup.embed-scan` uses `ResumeRequeue` — embedding is idempotent (already-embedded
  books are skipped cheaply).
- `maintenance.purge-deleted` uses `ResumeDrop` — a 30-minute cleanup window; if
  the server restarts mid-run the next scheduled execution handles the remainder.
- `maintenance.bulk-write-back` uses `ResumeRestart` with checkpoints — file writes
  are non-idempotent, so a crash should resume from the last checkpoint, not restart.

---

## 4. Isolation

`Isolate: true` runs the operation as a subprocess. Use it when:

- The operation runs for hours (e.g., full library re-encode).
- It spawns external processes (ffmpeg, AtomicParsley) that can leak file
  descriptors.
- A memory leak or panic in the operation must not affect the main server.
- You declare `CapSubprocessSpawn` — ops with that capability should be isolated.

`Isolate: false` (default) runs the `Run` function as an in-process goroutine.
This is appropriate for most operations: database scans, API calls, quick
maintenance tasks.

When `Isolate: true`, the default timeout increases from 120 minutes to 6 hours.
Both are capped at 24 hours. Set an explicit `Timeout` if your operation has
tighter bounds.

---

## 5. Capability Declarations

Capabilities are a coarse-grained permission vocabulary declared statically in
`OperationDef.Capabilities`. They are lint-enforced today and will be
runtime-enforced in a future release.

| Capability              | Meaning                                    |
|-------------------------|--------------------------------------------|
| `CapLibraryRead`        | Read book records from the database        |
| `CapLibraryWrite`       | Write/update book records                  |
| `CapFilesRead`          | Read from the filesystem                   |
| `CapFilesWrite`         | Write to the filesystem                    |
| `CapFilesExecute`       | Execute subprocess binaries                |
| `CapNetworkOpenAI`      | Call the OpenAI API                        |
| `CapNetworkAudible`     | Call the Audible API                       |
| `CapNetworkOpenLibrary` | Call the Open Library API                  |
| `CapNetworkGoogleBooks` | Call the Google Books API                  |
| `CapNetworkITunes`      | Communicate with the iTunes/Music service  |
| `CapNetworkGeneric`     | Other external network calls               |
| `CapScheduleCron`       | Op has a cron schedule                     |
| `CapScheduleEvent`      | Op fires on an event subscription          |
| `CapSubprocessSpawn`    | Spawns external processes                  |
| `CapDBMigrate`          | Runs database schema migrations            |

Declare the minimal set your operation actually uses. Over-declaration is a code
smell; under-declaration will fail CI once runtime enforcement lands.

---

## 6. Schedules, Triggers, and Ad-hoc Invocation

### Scheduled operations

Set `Schedule` to a cron expression (5-field, UTC). The registry runs the op
automatically:

```go
sched := "0 3 * * *"   // 03:00 daily, UTC
sdk.OperationDef{
    ID:           "myplug.nightly-sweep",
    Schedule:     &sched,
    Capabilities: []sdk.Capability{sdk.CapScheduleCron},
    // ...
}
```

Ops with a `Schedule` should also declare `CapScheduleCron`.

### Event-triggered operations

Subscribe to an event on the internal bus with `Triggers`:

```go
sdk.OperationDef{
    ID: "myplug.on-import",
    Triggers: []sdk.EventSubscription{
        {
            EventName: "book.imported",
            Handler: func(ctx context.Context, payload any) error {
                // payload is the event data
                return nil
            },
        },
    },
    Capabilities: []sdk.Capability{sdk.CapScheduleEvent},
    // ...
}
```

The event bus wiring (`Reporter.Trigger`) is implemented in UOS-05. Until then,
`Trigger` calls are accepted but do not fan out.

### Ad-hoc invocation

Any op can be started by calling `Registry.EnqueueOp`:

```go
runID, err := registry.EnqueueOp(ctx, "myplug.scan",
    json.RawMessage(`{"mode":"full"}`),
    sdk.WithActor(userID),
    sdk.WithPriority(sdk.PriorityHigh),
)
```

`WithParent`, `WithActor`, and `WithPriority` are the available option
constructors.

---

## 7. Testing Your Plugin

### Unit tests: OperationDef shape

The cheapest test verifies registration and OperationDef contract compliance.
Use a `stubRegistry` that records which ops were registered:

```go
type stubRegistry struct{ ops []sdk.OperationDef }

func (r *stubRegistry) RegisterOp(d sdk.OperationDef) error {
    r.ops = append(r.ops, d)
    return nil
}
func (r *stubRegistry) EnqueueOp(_ context.Context, _ string, _ any, _ ...sdk.EnqueueOption) (string, error) {
    return "", nil
}

func TestPlugin_Register(t *testing.T) {
    p := myplug.New(/* stub deps */)
    r := &stubRegistry{}
    if err := p.Register(r); err != nil {
        t.Fatalf("Register: %v", err)
    }

    // Check every op has an explicit ResumePolicy.
    for _, op := range r.ops {
        if op.ResumePolicy == sdk.ResumeUnspecified {
            t.Errorf("op %s: ResumePolicy not set", op.ID)
        }
        if op.ID == "" || op.Plugin == "" {
            t.Errorf("op %s: missing ID or Plugin", op.ID)
        }
    }
}
```

### Table-driven OperationDef tests

For each OperationDef method, write a table-driven sub-test:

```go
tests := []struct {
    name        string
    def         func(*Plugin) sdk.OperationDef
    wantID      string
    wantResume  sdk.ResumePolicy
    wantCaps    []sdk.Capability
}{
    {
        name:       "scan",
        def:        (*Plugin).scanDef,
        wantID:     "myplug.scan",
        wantResume: sdk.ResumeRequeue,
        wantCaps:   []sdk.Capability{sdk.CapLibraryRead},
    },
    // ...
}
for _, tc := range tests {
    t.Run(tc.name, func(t *testing.T) {
        p := &Plugin{}
        d := tc.def(p)
        if d.ID != tc.wantID { t.Errorf("ID: got %q, want %q", d.ID, tc.wantID) }
        if d.ResumePolicy != tc.wantResume {
            t.Errorf("ResumePolicy: got %v, want %v", d.ResumePolicy, tc.wantResume)
        }
        // ...
    })
}
```

### Integration tests using a real registry

Wire up a real `registry.Registry` with in-memory deps to verify the `Run`
function executes without error:

```go
func TestScanDef_RunSmoke(t *testing.T) {
    store := testutil.NewInMemoryStore(t)
    p := New(store)
    // Build a minimal registry and enqueue.
    reg := registry.New(/* opts */)
    if err := p.Register(reg); err != nil {
        t.Fatal(err)
    }
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    runID, err := reg.EnqueueOp(ctx, "myplug.scan", nil)
    if err != nil {
        t.Fatal(err)
    }
    if err := reg.WaitForRun(ctx, runID); err != nil {
        t.Fatalf("run %s: %v", runID, err)
    }
}
```

See `internal/plugins/dedup/plugin_test.go` and
`internal/plugins/itunes/plugin_test.go` for real examples of the
stub-registry pattern used in production plugins.

---

## 8. Worked Example

Below is a complete, minimal plugin (~60 lines) that records per-book import
counts in a custom database table. It uses `CapLibraryWrite` and runs whenever
a scan operation is invoked ad-hoc or on schedule.

```go
// Package importcounter is an example UOS plugin.
// It records how many times each book has been scanned.
package importcounter

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "time"

    "github.com/jdfalk/audiobook-organizer/pkg/plugin/sdk"
)

// Store is the minimal interface the plugin needs from the database layer.
type Store interface {
    GetAllBooks(offset, limit int) ([]Book, error)
    IncrementImportCount(bookID string) error
}

type Book struct{ ID string }

// Plugin implements sdk.Plugin for import counting.
type Plugin struct{ store Store }

func New(store Store) *Plugin { return &Plugin{store: store} }

func (p *Plugin) ID() string      { return "importcounter" }
func (p *Plugin) Name() string    { return "Import Counter" }
func (p *Plugin) Version() string { return "1.0.0" }

func (p *Plugin) Register(r sdk.Registry) error {
    sched := "0 1 * * *" // 01:00 daily
    return r.RegisterOp(sdk.OperationDef{
        ID:              "importcounter.scan",
        Plugin:          "importcounter",
        DisplayName:     "Record import counts",
        Description:     "Increments the import counter for every book in the library.",
        ResumePolicy:    sdk.ResumeRequeue,
        DefaultPriority: sdk.PriorityLow,
        ConcurrencyKey:  "importcounter.scan",
        Cancellable:     true,
        Isolate:         false,
        Timeout:         30 * time.Minute,
        Schedule:        &sched,
        Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapScheduleCron},
        Run:             p.run,
    })
}

func (p *Plugin) run(ctx context.Context, _ json.RawMessage, rep sdk.Reporter) error {
    _ = rep.Log(slog.LevelInfo, "import-counter: starting")
    books, err := p.store.GetAllBooks(0, 0)
    if err != nil {
        return fmt.Errorf("load books: %w", err)
    }
    for i, b := range books {
        if rep.IsCanceled() {
            return context.Canceled
        }
        if err := p.store.IncrementImportCount(b.ID); err != nil {
            rep.Logger().Error("increment failed", "book_id", b.ID, "err", err)
        }
        _ = rep.UpdateProgress(i+1, len(books),
            fmt.Sprintf("processed %d/%d books", i+1, len(books)))
    }
    _ = rep.Log(slog.LevelInfo, fmt.Sprintf("import-counter: updated %d books", len(books)))
    return nil
}
```

**What this demonstrates:**

- Minimal `sdk.Plugin` implementation with all four interface methods.
- One `OperationDef` with explicit `ResumePolicy`, `ConcurrencyKey`, `Capabilities`,
  and `Schedule`.
- Cancellation check inside the loop (`rep.IsCanceled()`).
- Progress reporting with `rep.UpdateProgress`.
- Structured logging via `rep.Logger()` and `rep.Log`.
- Dependency injection through a narrow `Store` interface — the plugin does not
  import any concrete database package.

**Where to put it in the tree:**

Production plugins live under `internal/plugins/<name>/`. They import
`pkg/plugin/sdk` and nothing else from the public tree. All dependencies are
injected by the server at startup.
