<!-- file: docs/agent-tasks/platform-core/F2-10-health-readiness-core-registry.md -->
<!-- version: 1.1.3 -->
<!-- guid: 4019705c-90f7-4596-b9ca-155678fda298 -->
<!-- last-edited: 2026-11-05 -->

# F2-10 — The health readiness core (`ComponentID`, `Result`, `Reporter`, `Registry` with `Register`/`Ready`/`Wait`), its token route, and the kernel's own components `core.storage` and `core.secrets`

| Field | Value |
|---|---|
| ID | F2-10 |
| Release / flip | R2, foundation PRs (F2), **dark** except the token bridge, which carries no token until F3 raises one |
| Wave | W24 |
| Tier | **T2** (Sonnet / high): a small registry with a fixed API, two kernel-side components, one bridge into an existing ABO token source |
| Depends on | OWNER-03 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-10-health-registry
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md).

## Goal

Grow `github.com/falkcorp/mediacore/health` (R1 holds only the flight recorder there) with the **readiness core** of
05-health §5H.3.1: `Status`, `ComponentID`, `Result`, `Check`, `Registry` (`Register`, `Ready`, `Live`, `Wait`; plus
`Snapshot` only as far as `Ready` needs it internally), `Reporter`, `Reporters`, `ErrNotOwned`. The kernel builds one
`Registry` per App and registers its own components **`core.storage`** and **`core.secrets`** kernel-side; this PR
writes that kernel side in `core/kernel/health.go`, and the **boot call** that invokes it is F2-16's (the F2 wiring row
owns `core/kernel/boot.go` and `kernel.go`), so the tokens F3 raises (`storage_quarantine` with its `orphaned_refs` field on
`core.storage`; `secrets_undecryptable` and `secrets_keyring_not_exported` on `core.secrets`) have components to ride on
from F3's first boot (IMPL-12). Bridge the registry's tokens into HEAD's **authenticated** token source over R1-24's token
path (the split R1 made, 05-health §5H.3.1 "The unauthenticated token set, and the route before F8") in
`internal/server/handlers/system/handler.go`, and extend R0-06's `TestHealth_UnauthenticatedTokenSetGolden` (in
`internal/server/handlers/system/health_tokens_test.go`) with the F2 cells. No module handle (`Registrar.Health`, `Deps.Health`) lands before F5;
probes, `/livez`, `/readyz`, `/api/core/v1/health` and the other components land at F8 (05-health §5H.10).

## Spec to read first

05-health §5H.3.1 (the Go block: copy it; "The unauthenticated token set"; "Advisory tokens"), §5H.10 (the F2 row of
"Landing by release"; the `CountAuthors` → `core.storage` engine-ping row), §5H.6 (concurrency), §5H.9 rows
`TestRegistry_Wait_UnblocksOnUp` and `TestHealth_UnauthenticatedTokenSetGolden`; 05-tasks §5T.3.1 (`ReadySignal =
health.ComponentID`, default `{core.storage}`), §5T.4 step 2; 06 §6.4.10 F2 row (kernel only, no `app` member).

## Base state: re-verify before editing

```bash
ls core/health/*.go                                                  # R1-09's flight recorder files, else STOP
grep -rn 'type ComponentID ' core/health/ | wc -l                    # 0 (this PR adds it); 1 = applied
test ! -e core/health/registry.go && echo absent                     # "absent"
grep -n 'type DegradedSource interface' internal/server/handlers/system/handler.go   # 1 hit at ac4368bdb (HEAD's source)
grep -n 'getDegraded' internal/server/handlers/system/handler.go | wc -l             # 6 at ac4368bdb; R1 splits it (count changes)
# R1-24's split: how core-raised posture tokens (storage_checkpoint_*, factory_reset_*) reach the authenticated field
grep -rn 'storage_checkpoint_open' core/ internal/server/ | grep -v _test | head -5   # must show the R1-24 path, else STOP
grep -c 'func TestHealth_UnauthenticatedTokenSetGolden' internal/server/handlers/system/health_tokens_test.go   # 1 (R0-06), else STOP
```

**STOP rule for the bridge.** 05-health says R1 split HEAD's `getDegraded` source in two and that core's R1 posture
tokens (`storage_checkpoint_open`, `factory_reset_failed`, …) already travel to the authenticated `degraded` field of
`GET /api/v1/system/status`; R1-24 built that path (the kernel's token source, injected by `internal/books/module.go`).
Find it with the `storage_checkpoint_open` grep. The registry's tokens must use **the same path**. If no
such path exists at your base (core tokens do not reach the handler), STOP and report: this PR must not invent a new
app-level member (06 §6.4.10: no `app` member at F2).

## Files

Create: `core/health/registry.go`, `core/health/registry_test.go`, `core/health/reporter.go`, `core/kernel/health.go`
(+ its test `core/kernel/health_test.go`).
Modify: `internal/server/handlers/system/handler.go` (the bridge only) and
`internal/server/handlers/system/health_tokens_test.go` (R0-06's table: add the F2 cells; R1-24's cells stay). No boot
file: the call from the kernel's boot sequence into `core/kernel/health.go` is F2-16's.

## Code contract

Copy 05-health §5H.3.1's Go block: `Status` (`StatusUnknown`, `StatusUp`, `StatusDegraded`, `StatusDown`),
`ComponentID` (`"<moduleID>.<name>"`), `Result{Status, Reason, Detail, At}` with `Reason` a fixed token
`[a-z0-9_]{1,40}`, `Check{ID, Title, Critical, Readiness, Probe, Interval, Timeout}`, `Registry` with
`Register(c Check) (Reporter, error)` (duplicate ID → error), `Ready() (bool, []string)` (tokens
`"<component>:<reason>"`), `Live() bool`, `Wait(ctx, ids...) error` (blocks until every listed component is Up or
Degraded; returns `ctx.Err()` on cancel), `Reporter`, `Reporters`, `ErrNotOwned`. `Snapshot`/`ComponentState` types may
be declared; their HTTP use is F8's. Add `func NewRegistry() *Registry` (name coined here). Probes with `Probe != nil` are
**not** scheduled in this PR (the probe loop is F8's); a push-only `Reporter` is the only update path now, plus one
synchronous call of `Probe` at `Register` time for `core.storage` (below).

No package-level mutable state (R5): one `Registry` per App. `Wait` must not busy-poll: use a condition or channel that
`Report` signals (CLAUDE.md: no unbounded goroutines).

Kernel (`core/kernel/health.go`), values chosen here because the spec states only that both components exist from F2:

| Component | Check fields | Initial result |
|---|---|---|
| `core.storage` | `Critical: true`, `Readiness: true`, `Probe`: a cheap engine ping on the kernel's open store (05-health §5H.10: replaces `CountAuthors`) | the probe's result at `Register` |
| `core.secrets` | `Critical: false`, `Readiness: false`, `Probe: nil` (push-only) | `Result{Status: StatusUp, At: now}`; the kernel keeps the `Reporter` for F3-03's `secrets` to raise its tokens |

The kernel-side function (name coined here, e.g. `buildHealth(store) (*health.Registry, kernelReporters, error)`) returns
the `*health.Registry` and both `Reporter`s; F2-16 calls it at boot and keeps them on the kernel for later phases (the executor's `Ready` wait,
F3's storage and secrets tokens). It exposes nothing on `app.App`.

Bridge (`handler.go`): through the R1 path found above, every component whose current `Result.Reason` is non-empty
contributes its `Reason` to the **authenticated** token list of `GET /api/v1/system/status`, and **never** to `/health`'s
unauthenticated `degraded` list (none of the registry's tokens before F8 is in the unauthenticated golden set,
05-health §5H.3.1). Sorted, de-duplicated, like the existing list.

## TDD: tests to write first

- `TestRegistry_Wait_UnblocksOnUp` (05-health §5H.9): in this PR, a component pushed Up through its `Reporter`
  (the spec's cell names `books.memdb` through `Deps.Health`, which lands at F5; use a test component and note that the
  `Deps.Health` cell is F5's) releases a waiting `Wait`; `Wait` on a cancelled ctx returns `context.Canceled`.
- `TestRegistry_RegisterDuplicateRefused`, `TestRegistry_ReadyTokens`, `TestRegistry_ReasonGrammar` (names coined here):
  duplicate ID → error; `Ready` lists `"<component>:<reason>"` for a Readiness component that is Down or Unknown; a
  `Reason` outside `[a-z0-9_]{1,40}` is refused (or clamped to a fixed token; pick one, document it).
- `TestKernel_RegistersStorageAndSecretsComponents` (name coined here, in `core/kernel/health_test.go`): the kernel-side
  function over an in-memory store returns a registry with exactly `core.storage` (Up after the ping) and
  `core.secrets` (Up). F2-16 adds the boot-path assertion.
- `TestHealth_UnauthenticatedTokenSetGolden` (05-health §5H.9; R0-06 created it, R1-24 added its cells): add the F2
  cells to its table: raise `storage_quarantine` on `core.storage` and `secrets_undecryptable` and
  `secrets_keyring_not_exported` on `core.secrets`; all three appear on the authenticated `GET /api/v1/system/status`
  field and none on `/health`'s unauthenticated `degraded` list.
- `TestNoGlobals_Health` (05-health §5H.9): extend to `registry.go`/`reporter.go` if the test exists; else add it for
  `core/health`.

## Steps

1. Base-state greps; locate R1's posture-token path; plan in the PR body.
2. Tests first.
3. Registry and reporter; kernel components; bridge.
4. Headers; changelog fragment; acceptance.

## Acceptance

```bash
cd core
go build ./... && go vet ./health/... ./kernel/...                                 # exit 0
go test ./health/... ./kernel/... -race -count=1                                   # ok
go test ./health/ -run TestRegistry_Wait_UnblocksOnUp -race -count=1 -v | grep -c '^--- PASS'   # 1
cd ..
go test ./internal/server/handlers/system/... -count=1                             # ok
go test ./internal/server/handlers/system/ -run 'TestHealth_UnauthenticatedTokenSetGolden' -count=1 -v | grep -c '^--- PASS: TestHealth_UnauthenticatedTokenSetGolden'   # 1
make -C core ci                                                                    # exit 0
make ci                                                                            # exit 0
```

## Out of scope

The boot call (F2-16); probes and the probe loop, `Snapshot` over HTTP, `/livez`, `/readyz`, `/api/core/v1/health`, other core components,
`Startup`, the watchdog ping, diagnostics (F8); `Registrar.Health`/`Deps.Health` and `books.memdb` (F5); raising any token
(F3 onward).

## Idempotency / rollback

If `core/health/registry.go` exists with `func (r *Registry) Wait`, run the acceptance instead. Rollback = revert: the
bridge then carries nothing, as before.

## Repo rules

File headers (keep the existing guid of `handler.go`, bump its version and `last-edited`); conventional commit
(`feat(core/health): readiness registry with the kernel's storage and secrets components`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment. `handler.go` is
also edited by R0-06, R1-11 and R1-24; rebase on the latest `main` before you start.

## Planner notes

- The component settings for `core.storage` and `core.secrets` (Critical, Readiness, initial result) are chosen in this
  brief; the spec states only that both exist from F2.
