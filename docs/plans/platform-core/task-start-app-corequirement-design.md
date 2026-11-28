<!-- file: docs/plans/platform-core/task-start-app-corequirement-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: b87e569b-e3b4-4bd0-9fb1-e7e465d2b3a7 -->
<!-- last-edited: 2026-11-28 -->

# App co-requirement on task Start (option (c)): design

Status: design only. This note changes no code. The build waits for the owner's
answer to item 20, which may choose a simpler route. If the owner has not
answered by the time this note is reviewed, the build goes ahead on this design.

## Code basis

| What | Ref |
|---|---|
| HEAD (behaviour to keep) | `origin/main` 5f35012b9 |
| F5-18 tasks flip | `agent/platform-core-f5-18-flip-r2` 40fa27eb2 |
| F5-18 B1 fix set (the tip read here; it contains 40fa27eb2) | `agent/platform-core-f5-18-b1-fixset` 9ae4c764b |
| W6 precedent | PR #5086 at 557655771 (`agent/platform-core-w6-app-corequirement`) |

Every `file:line` below is at 9ae4c764b unless marked HEAD or W6.

**The generalised `SystemStart` is not in code yet.** Every F5-18 and F6-03 branch
was checked on 2026-11-28. Only `Perms.OutboxSystemStart` exists
(`core/tasks/perms.go:35-53`, `core/internal/executor/start.go:231-232,293`). The
four B1 origins (scheduler, outbox target, same-module system Enqueue, core-service
`StartIn`) are therefore defined here **by structure**: what actor each start
carries and whether it carries a request checker. They are not defined by a flag
that does not exist yet. Section 6.3 explains why this still holds once B1 adds an
origin field.

## 1. Summary

- A new app-level table, `app.Options.TaskStartCoRequirements []tasks.StartCoRequirement`,
  gives every registered task kind exactly one row. A row is either a floor (`Need`,
  an AllOf of app scopes) or `None` with a reviewed reason.
- The floor lives **outside** `tasks.Permissions`. `Perms.Start`, `Cancel`, `Approve`
  and the system actor's `systemPerms` never see it. The cancel fallback, the decide
  fallbacks and SEC2-15 are therefore untouched by construction.
- The floor is judged **separately** from `Perms.Start`: two `Satisfies` calls, both
  of which must pass, never one merged AllOf. This is W6's rule, and it has the same
  reason: the auth-disabled checker would otherwise widen.
- It is enforced in one place, the executor's `permitStart`. It runs only when the
  start carries a request checker **and** the actor is not a module system actor.
  That covers `Client.Start` (the generic route, owner routes, and `ModuleClient`
  inside a request) and the apply run that `Approve` starts.
- The task HTTP handlers check the floor again before calling the executor, for the
  answer's shape only. The refusal is a 403 `permission_denied` whose
  `required_permission` names the missing floor scope, plus a `Denied` audit record.
  `can_start` and `can_approve` report false when the floor fails.
- It never applies to cancel, discard, resume, finish, decide or reject. It never
  applies to any start whose actor is a module system actor: scheduler, window
  members, outbox, same-module system Enqueue, core-service `StartIn`.
- A child that `rc.Enqueue` starts under a user-started parent is not judged. This is
  the rule `Perms.Start` already follows there, because no checker exists in a run.
  The parent's floor was the decision point, which is HEAD parity.
- A user's start of a window kind does not judge its members' floors. HEAD's
  `/maintenance-window/run` asked only for settings.manage.
- P7 boot validation follows W6: AllOf only, registered app scopes, no core scope,
  no Sensitive or Confining scope, a known kind, no duplicates. It also adds
  **completeness**: every registered kind needs a row, and boot fails naming every
  kind that has none.
- ABO's table has 134 `books.*` rows (60 `books:scan`, 3 `books:edit`, 71 `None`)
  and 7 rows for the static core kinds (2 floors, 5 `None`). `core.window.<name>`
  kinds take core's built-in exemption.
- Cost: core about 2.3 days, ABO about 0.6 day. The 120 kinds not yet registered get
  their rows in the PR that registers each one, and P7 enforces that.

## 2. The problem, with counts reconciled

The tasks flip drops part of HEAD's start gate on some kinds. HEAD's fresh user start
of these kinds went through a route that asked for `scan.trigger` (`books:scan`).
For the 3 Repairs kinds it asked for `library.edit_metadata` (`books:edit`). The
native `Perms.Start` of each kind keeps only the settings or users scopes. A principal
holding exactly those scopes and nothing more can exist through any of these:

- a custom role;
- a legacy API key holding a subset of its owner's atoms;
- a native core key.

No scope implies `books:scan`. Such a principal can start these kinds natively, but
could not at HEAD.

**Counts.** The F5-18 kinds dump at 40fa27eb2 has **134 rows, all `books.*`**. No
core kind is in it. The class is:

| Group | Kinds | HEAD scope the native Start drops |
|---|---|---|
| A: ABO kinds whose HEAD start was ops v2 / v3 (`scan.trigger`) | 58 | `books:scan` |
| B: native-only batch children (`batch_children.go:120,209,434`) | 3 | `books:scan` (their parent's) |
| C: Repairs kinds (`internal/repairs/kinds.go:194`) | 3 | `books:edit` |
| D: core kinds `core.integrations.release-holds`, `core.storage.census` | 2 | `books:scan` (+ `books:settings.manage` for census) |

That is **64 ABO + 2 core = 66**. The sizing's phrase "66 of the 134 registered
kinds" counts the 2 core kinds inside the 66, although they are not among the 134
dump rows. The brief's phrase "the 66 class kinds plus the 2 core kinds" counts them
twice. This note uses 64 + 2. That leaves **70 ABO kinds outside the class**: 20
Repairs kinds whose Start already names `books:edit`, plus 50 kinds whose HEAD start
was a settings-only route (35 manual, 15 scheduled).

## 3. The API

### 3.1 Type (core, `core/tasks/corequirement.go`, new)

```go
// StartCoRequirement is one row of the app's task-start co-requirement table.
// Exactly one of Need and None is set.
type StartCoRequirement struct {
    Kind Kind
    Need rbac.Requirement // AllOf (or Need) over app scopes: the floor
    None string           // reviewed reason this kind has no floor
}
```

The same file holds the boot sentinels (section 3.4) and one pure validator. Its
signature: `CheckStartCoRequirements(rows []StartCoRequirement, known func(Kind) bool,
registered []Kind, exempt func(Kind) bool, cat *rbac.Catalog) []error`. Core owns
the validator, as W6's `checkCoRequirement` lives in `httpapi`, so the kernel and
`coretest` judge rows the same way.

### 3.2 Where it is declared: `app.Options`, not the Registrar

The table is the **app's**. It goes beside W6's `app.Options.RouteCoRequirements`.

- **It must name app scopes on core kinds.** Two kinds of class D are core kinds.
  Core cannot name `books:*` in its own definitions (the R3 rule enforced at P7,
  `kernel/tasks_checks.go:104-105`), and an app cannot reach core's Registrar
  (`kernel/core_kinds.go:116`). Only an app-level table can give
  `core.storage.census` a `books:scan` floor.
- **A per-module Registrar field would be the hybrid under another name.** A module
  that can state a floor on its own kind can already state it in `Perms.Start`.
  What (c) adds is that the floor sits *outside* Perms, and that the app reviews one
  table covering every module and core.
- **How core knows the kind set:** it reads what P7 already reads.
  - Registered kinds are `k.taskDefinitions()` (`kernel/tasks_checks.go:86-94`):
    enabled modules' `Registrar.Tasks`, plus core's own entry when the flip is on
    (`kernel/core_kinds.go:59-64,99-117`).
  - Known kinds are registered kinds ∪ every manifest's `TaskLedger`, enabled or
    not (the `taskStartProblems` precedent, `kernel/tasks_checks.go:310-319`) ∪ a
    new static list from core, `kernel.CoreTaskKinds()`. That list names every kind
    `registerCoreKinds` can register.
  - The interim kinds (`core_kinds.go:106-114`) are registered only when their
    backend is supplied. The static list lets a row name such a kind while it is
    absent, so the row does not fail boot. Describe, which skips `registerCoreKinds`,
    accepts the same rows for the same reason.

### 3.3 Plumbing, beside W6

| Step | W6 (#5086) | This design |
|---|---|---|
| Type | `httpapi.CoRequirement{Route, Param, Value, Need}` (`core/httpapi/corequirement.go:66-71`, W6) | `tasks.StartCoRequirement{Kind, Need, None}` |
| App field | `app.Options.RouteCoRequirements` (W6 `core/app/options.go:+19`) | `app.Options.TaskStartCoRequirements` |
| App → kernel | `kernelConfig` copies it (W6 `core/app/app.go:383`) | `kernelConfig` copies it (`core/app/app.go:363-384`, next to `Interim`) |
| Kernel config | `kernel.Config.RouteCoRequirements` | `kernel.Config.TaskStartCoRequirements` |
| Validated at | `Router.Build` → `applyCoRequirements` (W6 `corequirement.go:179-232`) | P7: new `k.startCoRequirementProblems()`, called from `taskProblems` next to `taskStartProblems` (`kernel/tasks_checks.go:127`); reported as `module.Problem{Kind: ProblemStartCoRequirement}` (new, `core/module/validation.go`) |
| Handed to | `httpapi.Options.CoRequirements` (W6 `kernel/boot.go:323-329`) | `constructTasks` (`kernel/tasks.go:361-439`) builds one validated `map[tasks.Kind]rbac.Requirement` holding floor rows only. It passes the map to `executor.Options.StartCoRequirements` (`tasks.go:376`) and to `taskhttp.Deps.StartCoRequirements` (`tasks.go:421`). |
| Judged by | stage 13, `AuthzRequest.CoPerms`, one `coPermRefusal` per rule after `Perm` (W6 `stage13.go:+142-152`) | `permitStart` (section 4), one more `Satisfies` call after Start's |
| Who is judged | every HTTP request on the route | user, API-key and auth-off actors with a request checker; never a module system actor |
| Completeness | none (an unlisted route needs nothing) | every registered kind has a row (section 3.4) |
| Display | `routeEntry.perm()` + OpenAPI `x-mediacore-co-requirements` (W6 `corequirement.go:134-149`, `internal/openapi/openapi.go:+70`) | `can_start`, `can_approve`, and the same OpenAPI extension on `startTaskRun` with `param: kind` (section 7) |

`executor.New` refuses a map entry that is zero or not AllOf, or that names a kind
missing from `Definitions`. This is defence in depth: the kernel has already
filtered the map.

ABO sets the field in `books.Options` **only when `tasksFlipOn()`**, in the same
block as `InterimBackends` (`internal/books/options.go:180-188`). With the flip off,
no core kind is registered and no row has anything to apply to. An ABO test pins
that the field is non-nil whenever the flip is on.

### 3.4 Boot validation (P7), each a refusal that names the row

| # | Refused | Sentinel (core/tasks) | W6 twin |
|---|---|---|---|
| 1 | Row names a kind that is not known (not registered, not in any `TaskLedger`, not in `CoreTaskKinds()`) | `ErrStartCoRequirementKind` | `ErrCoRequirementRoute` |
| 2 | Row names a `core.window.<name>` kind (core owns that exemption) | `ErrStartCoRequirementKind` | none |
| 3 | Two rows for one kind | `ErrStartCoRequirementDuplicate` | `ErrCoRequirementDuplicate` |
| 4 | Both or neither of `Need` / `None` | `ErrStartCoRequirementShape` | none |
| 5 | `Need` not AllOf over ≥ 1 scope (AnyOf, legacy, zero) | `ErrStartCoRequirementShape` | `ErrCoRequirementShape` |
| 6 | `Need` is Public / AnyAuthenticated, or names `core:admin` or a reserved (core) namespace | `ErrStartCoRequirementScope` | `ErrCoRequirementScope` |
| 7 | A scope the catalog does not declare, or no catalog | `ErrStartCoRequirementUnregistered` | `ErrCoRequirementUnregistered` |
| 8 | A scope the catalog flags Sensitive or Confining | `ErrStartCoRequirementRestricted` | `ErrCoRequirementRestricted` |
| 9 | **Completeness**: a registered kind with no row, unless it is a `core.window.<name>` kind; one error that lists every such kind | `ErrStartCoRequirementMissing` | none |

- **Catalog for rows 7 and 8:** `k.executorCatalog()` (`kernel/tasks.go:253-266`),
  which declares core's scopes and every enabled module's `Manifest.Permissions`.
  AuthRequired is allowed, as in W6, because it only narrows the auth-off principal.
- **When completeness is armed:** whenever the field is non-nil. A nil field means
  the app uses no co-requirements, exactly like W6's nil list. ABO's non-nil pin
  (section 3.3) is what stops a dropped field from passing silently. Open question 1
  asks whether core should require the field from every app that has `TasksBoot`.
- **Window kinds.** `core.window.<name>` is config-driven
  (`kernel/core_kinds.go:105`, `scheduler.WindowDefinition`). Its start is judged
  against every member's `Perms.Start` (`permitWindow`, `start.go:260-278`). Core
  supplies its row as a built-in, stated reason in the validator, so the row is
  explicit, not a silent default. An app row naming a window kind is refused (row 2),
  so nobody believes such a row does something.
- **Describe** runs P7 (`taskProblems` is pure) but not `registerCoreKinds`. It
  checks completeness over module kinds only, and it accepts core-kind rows through
  `CoreTaskKinds()`. A real boot also checks the core kinds. CI's describe run
  therefore catches a missing row for any new ABO kind.

## 4. Evaluation points

### 4.1 The rule

Insert after the Start check in `permitStart` (`core/internal/executor/start.go:242-244`):

> if the start has a request checker (`s.who != nil`) and the actor is not a module
> system actor (the early return at `start.go:239-241` already left), and the kind
> has a floor `F`: when `!s.who.Satisfies(F)`, return a `*StartCoRequirementError{Kind,
> Missing, AuthOff}` that unwraps to `ErrNotPermitted`.

The gate is keyed on the **checker and the actor**, not on the actor's method alone:

- `taskhttp.Principal` never returns a nil checker for a request (`holdsNothing`,
  `core/internal/taskhttp/principal.go:26-36`).
- It never stamps `System` (`principal.go:40-49`). A session, an API key (`token`) or
  the auth-off principal always reaches the floor.
- An `rc.Enqueue` child carries its parent's actor with `s.who == nil`
  (`attempt.go:326,345`), so a child of a user-started parent does not reach it.

`Missing` is the first scope of `F`, in declared order, that `s.who.Has` refuses.
`AuthOff` is set when the actor's method is the auth-disabled one.

### 4.2 Every start path, and whether the floor applies

| # | Path | Actor / checker | Floor? | file:line | Reachable at 9ae4c764b |
|---|---|---|---|---|---|
| P1 | Generic route `POST /definitions/{kind}/runs` → `Client.Start` | request principal | **yes**: handler pre-check (section 4.3) + `permitStart` | `taskhttp/start.go:80-135`; `executor/start.go:53-71` | built, **not mounted**: `kt.server` is assigned at `kernel/tasks.go:437` and never routed. F2-16 mounts it. |
| P2 | Approve accept → apply run → `prepare(modeApprove)` | request principal | **yes**: handler pre-check + `permitStart` | `taskhttp/control.go:185-244`; `executor/control.go:419-498` (apply `prepare` at `:470`) | built, not mounted (same) |
| P3 | Repairs plan (a Preview start) and Repairs approve | as P1 / P2 | **yes** (as P1 / P2) | `internal/repairs/kinds.go:194` (Start = Approve = View) | only through P1 / P2. HEAD's opsv3 approver (`internal/server/handlers/opsv3/runs.go:454`) is HEAD's own path, not the executor. |
| P4 | Module `Client.Start` (`Deps.Tasks`, `ModuleClient`) inside a request | the request principal wins over the module key | **yes**: `permitStart` | `executor/module_client.go:38-48,105-111` | yes. No ABO caller of `tasks.Client` exists at this tip (grep of `internal/` and `cmd/`). Future owner routes (F5-18 (i) user-merge) and any re-pointed ops route land here. |
| P5 | Core owner routes: `core.diagnostics.export`, `core.updater.*` | request principal | **yes**: `permitStart` (their rows are `None`; section 8) | `kernel/diagnostics_wire.go:127`; `kernel/updater.go:182` | yes |
| P6 | User start of a window kind | request principal | **window kind: no row** (core exemption). **Members: not judged** (deliberate, section 4.4). | `executor/start.go:260-278` | via P1 |
| P7 | `rc.Enqueue` under a **user** actor (batch children and every child) | user actor, `s.who == nil` | **no**: inherits (section 4.4) | `executor/attempt.go:321-357`; callers: `maintenance/batch_sweep.go:463` (the 3 `.batch` children), `dedup/kinds_chain.go:91` (`acoustid.fingerprint-rescan`), `:159,161` (dedup children), `maintenance/optimize.go:257` (group-6 children) | yes |
| S1 | Scheduler | system actor via `StartIn` | **no** | `internal/scheduler/scheduler.go:455`; actor stamped at `executor/start.go:81` | yes |
| S2 | Window members at run time | system actor via `StartIn` | **no** | `internal/scheduler/window.go:136` | yes |
| S3 | Outbox target (`ConsumeIntoTask`) | owner's system actor, `s.outbox` | **no** | `core/outbox/consumer.go:97`; `executor/start.go:83,293` | yes |
| S4 | Same-module system Enqueue | system actor (parent's), `s.who == nil` | **no** | `attempt.go:326` → `permitStart` system branch `start.go:239-241` | yes |
| S5 | Core-service `StartIn` (AI batch apply) | system actor | **no** | `core/integrations/ai/batchpoll.go:343` | yes |
| S6 | Kernel-internal `Client.Start` with no principal | none (`who == nil`) | **no** (Start is not judged there either) | `executor/module_client.go:47` | yes |
| S7 | P7 `CheckOutboxStart` | system actor | **no** | `executor/start.go:305-316` | boot |

**User-drivable `StartIn`.** Every `StartIn` caller in the tree is S1, S2, S3 or S5
(grep at 9ae4c764b: those four call sites and nothing else). The kind each one
starts is fixed by registration, not by the request:

- S1: the kind's own `Schedule`;
- S2: window membership from config;
- S3: the `ConsumeIntoTask` binding, checked at P7;
- S5: the apply kind minted per AI capability (`ai/batch.go:489`).

A request can make one of these happen, for example a write that stages an outbox
message, but it cannot choose the kind. They are system origins, as the F5-18 lead
requires.

**Schedules.** A user cannot point a schedule at a floored kind.
`PATCH /schedules/{kind}` acts only on a kind that declares a `Schedule`
(`taskhttp/schedules.go:33`), and none of the 64 ABO class kinds declares one (all 64
have `sched=false` in the dump). Open question 4 asks whether P7 should refuse a
floor together with a `Schedule`.

### 4.3 The handler checks (shape, not enforcement)

- **`startRun`** (`taskhttp/start.go:93-107`). The floor check goes **between** the
  Start check (`:95-98`) and the `OwnerRouteOnly` answer (`:102-106`). The rule in
  the comment at `:99-101` still holds: a caller short of any part of the permission
  gets the permission answer, and only a caller who may start the kind learns that it
  has an owner route.
- **`approveRun`** (`taskhttp/control.go:226-232`). The check goes after
  `verifiedGate` (`:226`), **only when `!b.Reject`**. Reject is a decision and keeps
  HEAD's gate.
- **`definitionView`** (`taskhttp/definitions.go:54-56`):
  - `CanStart = Satisfies(Start) && Satisfies(floor) && OwnerRouteOnly == ""`;
  - `CanApprove = Satisfies(Approve) && Satisfies(floor)`, because approving starts
    the apply run. Open question 3 covers what this means for reject.
- **`mapError`** (`taskhttp/problem.go:110`). `errors.As(*StartCoRequirementError)`
  goes **before** the `ErrNotPermitted` case, so any executor-side floor refusal (P4,
  P5, or a handler check that drifted) still answers with `required_permission`.

The executor stays the enforcement point. The handlers only shape the answer. A
mutant that deletes a handler check is caught by the shape cells; one that deletes
the `permitStart` check is caught by P4 and the direct-`Approve` cells (section 9).

### 4.4 Deliberate non-points

| Not judged | Where | Why |
|---|---|---|
| Cancel / Discard | `executor/control.go:178-206` (`mayCancel` fallback `:202-205`), Discard `:508` | HEAD gated them at settings.manage only. The fallback reads `Perms.Cancel`, else `Perms.Start`, and never the floor. |
| Decide (resume, finish, drop) | executor `control.go:319-343` (fallback `:337-340`); handler `taskhttp/control.go:276-282` | same, and the fallback reads `Approve`, else `Start` |
| Approve **reject** | `executor/control.go:455-458` | a decision, not a start |
| Executor resume sweep, `DecideAs` | `executor/resume.go`, `control.go:355-` | system, or not a fresh start |
| Window members on a user's window start | `permitWindow`, `start.go:260-278` | HEAD's `POST /maintenance-window/run` asked only for settings.manage (HEAD `wire_operations_routes.go:123`), so judging members' floors would narrow. 0 of the 15 scheduled ABO kinds is in the class. Open question 2. |
| `rc.Enqueue` child under a user actor | `attempt.go:345` (`s.who == nil`) | No checker exists in a run, which is the rule `permitStart`'s doc states for Start (`start.go:224-228`). At HEAD the indirect start was gated by the **parent's** route. Example: `dedup` lsh-index-build queued `acoustid.fingerprint-rescan` at HEAD too (`dedup/kinds_chain.go:84-91`). Judging the child against its own floor would narrow HEAD. Open question 5. |

## 5. Refusal shape and audit

| Point | Error (Go) | HTTP answer | Audit |
|---|---|---|---|
| `permitStart` | `*executor.StartCoRequirementError{Kind, Missing, AuthOff}`, `Unwrap() == ErrNotPermitted`; message `start <kind>: the app's start co-requirement <F> is not held (missing <scope>)` | via `mapError`: 403 `permission_denied`, `required_permission: <Missing>`; when `AuthOff`, 403 `interactive_required` with no scope, the shape of W6's `AuthOffRefusal` (`core/httpapi/authoff.go:35-37`) | The executor has no audit sink (by design). The calling route audits. For P4, ABO owner routes must map the typed error (risk 7). |
| `startRun` | (pre-check) | 403 `permission_denied`, `required_permission: <Missing>` (the floor's scope, never `Perms.Start.String()`); auth-off as above | **New**: a `Denied` record. Action `core.task.start`, target `core/task_definition/<kind>`, `Permission: <Missing>`, reason `co_requirement_not_held`, through `deny` (`taskhttp/control.go:75-86`), with a target variant for a start that has no run. |
| `approveRun` (accept) | (pre-check) | same | `Denied`: action `core.task.approve`, target the plan run, reason `co_requirement_not_held` (existing `deny`) |
| `can_start`, `can_approve` | n/a | n/a | none (display) |

`startRun` writes no denial record today, even for a Start refusal. This design adds
one for the floor. Auditing Start refusals there as well is a separate gap, and it is
not fixed here. It costs about 0.1 day if the coordinator wants it in the same build
(open question 6).

## 6. Interactions

### 6.1 SEC-9 (`StartableByViewers`)

SEC-9 judges `Perms.Start` alone (`viewStartScopes`, `kernel/tasks_checks.go:247`).
The floor does not count toward it and does not change it. A kind whose Start is
view-class still needs `StartableByViewers`, whatever its floor names. A view-class
floor scope is allowed because it only narrows. One cell pins that the floor is
ignored by the SEC-9 check.

### 6.2 `OwnerRouteOnly`

- The generic route answers in this order: Start, then floor, then
  `tasks.owner_route_only` (section 4.3).
- An owner route that calls `Client.Start` under the user principal goes through
  `permitStart`, so **the floor applies inside owner routes too**. An owner-route
  kind's row therefore records what the owner route's HEAD gate asked for beyond
  Start.
- `books.maintenance.user-merge` is in the class (group A), but its row is **`None`**.
  HEAD's only working start was the owner route: users.manage, owner, credential, and
  the admin role in the handler (HEAD `wire_library_routes.go:170-181`, where "Apply
  ... run ... only with a grant these handlers mint"). None of these asks for
  `books:scan`, so a `books:scan` floor would narrow that route. F5-18 (i) closes the
  generic route with `OwnerRouteOnly` and puts HEAD's gate on the owner route. The
  two compose.
- `core.diagnostics.export` (already `OwnerRouteOnly`) and `core.updater.*` are
  likewise `None`.

### 6.3 `SystemStart` (B1)

The floor needs no `SystemStart` input. Every B1 origin starts as a module system
actor (S1-S5), and `permitStart` returns at `start.go:239-241` before the floor. When
B1 lands an origin field on `starter`, the gate must **stay keyed on actor and
checker**, not on the origin. That is safe as long as B1 keeps "a system origin is
always a system actor" and refuses a caller-supplied origin on `Client.Start`, the
rule `starter.outbox` follows today (`start.go:35-39`). Open question 7 asks the
F5-18 lead to confirm this. Each origin has a bypass cell (section 9, cells C11a-e).

### 6.4 `Perms.Approve`

- The floor is never merged into `Approve`.
- Accept starts an apply run, and `Executor.Approve` already requires `Perms.Start`
  for it through `prepare` (`control.go:417-418,470`). The floor joins it there.
- Reject and decide are not judged.
- For the 3 class-C Repairs kinds, HEAD gated approve (including reject) **and View**
  with the same set that included `books:edit`. A Start-only floor restores the start
  and the accept. It does not restore reject or View (open question 8; not designed
  here: "fresh Start only").

### 6.5 Auth-off

The floor is judged with its own `Satisfies` call. The auth-disabled checker drops an
AllOf's AuthRequired members and refuses an AllOf made only of them
(`internal/authz/authz.go:294-313`). The consequences:

- (a) **The floor cannot widen.** With Start = AllOf(AuthRequired-only) and floor =
  AllOf(ordinary scope), a merged AllOf would drop the AuthRequired member and admit
  auth-off. Judged apart, Start refuses.
- (b) **A floor made only of AuthRequired scopes refuses auth-off**, which only
  narrows.

`books:scan` and `books:edit` are neither AuthRequired nor Sensitive
(`internal/books/identity_declarations.go:85-105`), so auth-off holds them
(`AuthDisabled`, `authz.go:167-180`) and passes ABO's floors. That is HEAD's
auth-off behaviour. The refusal shape follows W6 (section 5).

## 7. Display

- **Definitions listing** (`GET /definitions`, `/definitions/{kind}`): `can_start`
  and `can_approve` AND the floor (section 4.3). No new DTO field. The DTO's rule
  "the scopes it needs are not listed" (`definitions.go:30-32`) stays.
- **OpenAPI.** taskhttp emits no OpenAPI today because its routes are unmounted.
  When F2-16 mounts `startTaskRun` (`taskhttp/routes.go:187`), it carries W6's
  `x-mediacore-co-requirements` extension (W6 `internal/openapi/openapi.go:+70-92`)
  with one entry per floored kind: `{param: "kind", value: <kind>, permission: <F>}`.
  This is display only. Stage 13 does not judge it: the router does not attach W6
  rules for it, and the handler and executor judge it. About 0.1 day, in F2-16's
  mount PR or here if F2-16 has landed.
- **Describe.** P7 problems (including completeness) are part of Describe's output.
  The OpenAPI document Describe builds picks up the extension once the route is
  mounted.

## 8. ABO's table

**File:** `internal/books/task_start_corequirements.go` (new). It holds a sorted
literal table, a handful of named reason constants, and
`taskStartCoRequirements() []tasks.StartCoRequirement`, which `books.Options` sets
when the flip is on.

| Rows | Count | Need / None |
|---|---|---|
| Group A minus user-merge | 57 | `AllOf(books:scan)` |
| Group B (`.batch` children) | 3 | `AllOf(books:scan)`: their parent's floor. A generic start of one is floored even before F5-18 (iii) makes them parent-or-system only. |
| `books.maintenance.user-merge` | 1 | `None`: reasonOwnerRoute |
| Group C (reconcile.missing-files, audible-read-status, master-list-import) | 3 | `AllOf(books:edit)` |
| Repairs kinds whose Start is `books:edit` | 20 | `None`: reasonStartCarriesHEAD |
| Settings-route kinds (35 manual + 15 scheduled; includes F5-18 (ii)'s 16) | 50 | `None`: reasonSettingsRoute |
| `core.storage.census` | 1 | `AllOf(books:scan, books:settings.manage)` |
| `core.integrations.release-holds` | 1 | `AllOf(books:scan)`. Ruling 1's OperatorRequirement + `SystemStart` change its Start; this row is the `books:scan` residue. |
| `core.storage.compact`, `core.tasks.prune`, `core.log.journal.prune`, `core.ai.batch-poll` | 4 | `None`: reasonCoreNotInClass |
| `core.diagnostics.export` | 1 | `None`: reasonOwnerRoute |
| `core.window.<name>` | n/a | core's built-in exemption |

Totals: 134 `books.*` rows (60 floors `books:scan`, 3 floors `books:edit`, 71 `None`)
and 7 core rows.

The reason constants are short, reviewed sentences:

- `reasonSettingsRoute`: "HEAD's only fresh start was the settings-only route;
  Start already carries every scope it asked."
- `reasonStartCarriesHEAD`: "Start already names books:edit, HEAD's lane
  permission."
- `reasonOwnerRoute`: "Started only through its owner route, which carries HEAD's
  gate; a floor would narrow it."
- `reasonCoreNotInClass`: "Core kind whose HEAD start asked nothing beyond Start
  (sizing 2026-11-28)."

Sample rows (shape):

| Kind | Need | None |
|---|---|---|
| `books.acoustid.fingerprint-rescan` | `books:scan` | |
| `books.deluge.link-backfill` | `books:scan` | |
| `books.maintenance.extract-wav-clips.batch` | `books:scan` | |
| `books.maintenance.user-merge` | | reasonOwnerRoute |
| `books.repairs.reconcile.missing-files` | `books:edit` | |
| `books.repairs.duplicate-copies` | | reasonStartCarriesHEAD |
| `books.maintenance.purge-deleted` | | reasonSettingsRoute |
| `core.storage.census` | `books:scan`, `books:settings.manage` | |
| `core.tasks.prune` | | reasonCoreNotInClass |

**The 120 kinds not yet registered.** P7 completeness fails the boot, and
Describe fails CI, for any registered kind without a row. The PR that registers a
kind therefore adds its row in the same change. F5-18's t7 worker records each new
kind's HEAD start route in the dump format as it builds the kind. That record decides
the row:

- `books:scan` where HEAD's fresh start needed `scan.trigger` and the native Start
  does not carry it;
- `books:edit` likewise for `library.edit_metadata`;
- otherwise `None` with the matching reason.

An ABO unit test cross-checks the table against `taskLedgerKinds` and the static core
list, so the failure shows up before a boot.

## 9. Cells, each with a mutant

The core cells use Batman fixtures: module `gotham`, kinds `gotham.sweep` (floored)
and `gotham.patrol` (not floored), floor scope `gotham:watch`, AuthRequired scope
`gotham:vault`. The ABO cells use real kinds.

| # | Cell | Mutant that must FAIL it |
|---|---|---|
| C1 | `permitStart`, session user with Start, without floor → typed error, `Missing == gotham:watch`, `errors.Is(ErrNotPermitted)` | delete the floor check in `permitStart` |
| C2 | same user with Start + floor → starts | invert the floor test / always refuse |
| C3 | API-key actor (`token`) with Start only → refused; with both → starts | gate the floor on `actor.AuthMethod == "session"` |
| C4 | `ModuleClient.Start` inside a request (P4) → refused | skip the floor when ctx carries the module key |
| C5a | `startRun`: 403 `permission_denied`, `required_permission: gotham:watch`, `Denied` record `core.task.start` | remove the handler check (the executor's answer has no `required_permission`, no record) |
| C5b | `startRun`: the body names the floor scope, not Start's | set `Required = Perms.Start.String()` |
| C6 | `startRun` order: short of Start → Start's answer; Start held, floor missing, `OwnerRouteOnly` kind → floor answer, not `owner_route_only` | move the floor check after `:102-106` |
| C7a | `approveRun` accept, Approve + Start held, floor missing → 403 naming `gotham:watch` + `Denied` | remove the approve pre-check |
| C7b | direct `Executor.Approve` (no handler) → typed error | remove the floor from `permitStart` (C1's mutant, via the apply path) |
| C8 | `approveRun` **reject** without the floor → succeeds | apply the floor to reject |
| C9 | `can_start` / `can_approve`: false without the floor, true with it | drop the floor from `definitionView` |
| C10 | User starts a window whose member is floored, holding the members' Start but not the floor → **starts** (deliberate non-point) | add floors to `permitWindow` |
| C11a | Scheduler `StartIn` of a floored scheduled kind → starts | judge the floor for every start that has a floor, whatever the actor |
| C11b | Outbox `ConsumeIntoTask` of a floored kind → starts; P7 `CheckOutboxStart` passes | same |
| C11c | System-actor parent `rc.Enqueue` of a floored child → starts | same |
| C11d | Core-service `StartIn` (batch-apply shape) of a floored kind → starts | same |
| C11e | Scheduled window run starts a floored member → starts | same |
| C12 | User-actor parent `rc.Enqueue` of a floored child → starts. ABO twin: lsh-index-build → `acoustid.fingerprint-rescan` | refuse a floored child under a non-system actor |
| C13a | Holder of Cancel / Start but not the floor cancels and discards a floored run | fold the floor into `Perms.Start` at registration (the hybrid) |
| C13b | Holder of Approve / Start but not the floor decides resume, finish and drop | same, and separately: add the floor to `Decide` |
| C14a | P7: a registered kind with no row → boot fails naming it (and every other missing kind) | skip completeness |
| C14b | P7: a row for an unknown kind; a window-kind row; a duplicate; both or neither of Need/None → each refused | delete each rule (one mutant each) |
| C14c | P7: rows naming conditional core kinds, flip on with no interim backend, and under Describe → no failure | build "known" from the registered kinds only |
| C15 | P7: AnyOf; Public; AnyAuthenticated; `core:admin`; a `core:*` scope; an unregistered scope; no catalog; Sensitive; Confining → each refused | delete each rule (one mutant each) |
| C16 | `core:admin` principal passes a floored start | judge the floor by literal membership instead of `Has` |
| C17a | Auth-off, Start = AllOf(`gotham:vault`) (AuthRequired-only), floor = AllOf(`gotham:watch`) → refused | judge `Satisfies(AllOf(Start ∪ floor))` |
| C17b | Auth-off, floor = AllOf(`gotham:vault`) → refused with 403 `interactive_required` | answer auth-off with `permission_denied` + scope / admit it |
| C17c | Auth-off, ABO floor `books:scan` → passes (HEAD parity) | add `books:scan` to the AuthRequired set in the fixture catalog |
| C18 | SEC-9: a view-class Start with a manage-class floor is still refused without `StartableByViewers` | let the floor satisfy SEC-9 |
| C19a | ABO: a native custom role holding exactly the settings.manage expansion (8 scopes) → `books.maintenance.title-repair` refused, 403 `required_permission: books:scan` | change that kind's row to `None` |
| C19b | ABO: the same role + `books:scan` → 202 | make the floor `AllOf(books:scan, books:edit)` |
| C19c | ABO: settings role without `books:edit` → `books.repairs.reconcile.missing-files` refused naming `books:edit` | drop the class-C row |
| C20 | ABO table: kinds == `taskLedgerKinds` ∪ static core list; every class kind (except user-merge) floored as above; every non-class kind `None` | delete a row; add `books:scan` to a non-class kind (narrowing against HEAD) |
| C21 | ABO: `books.Options` sets the field iff `tasksFlipOn()` | set it with the flip off / drop it with the flip on |

## 10. Cost per file

| File | Change | Estimate |
|---|---|---|
| `core/tasks/corequirement.go` (new) + test | type, sentinels, validator | 0.4 d |
| `core/app/options.go`, `core/app/app.go` + test | field, `kernelConfig` copy | 0.1 d |
| `core/module/validation.go` | `ProblemStartCoRequirement` | < 0.1 d |
| `core/kernel/kernel.go`, `tasks_checks.go`, `core_kinds.go` + tests | Config field; `startCoRequirementProblems` (rows 1-9); `CoreTaskKinds()`; window exemption | 0.5 d |
| `core/kernel/tasks.go` | build the map; pass it to the executor and taskhttp | 0.1 d |
| `core/internal/executor/options.go`, `executor.go`, `start.go`, `errors.go` + tests | Options field; `New` check; floor in `permitStart`; typed error; cells C1-C4, C7b, C10-C13, C16-C17 | 0.6 d |
| `core/internal/taskhttp/routes.go`, `start.go`, `control.go`, `definitions.go`, `problem.go` + tests | Deps field; `startRun` / `approveRun` pre-checks + `Denied`; `can_*`; `mapError` | 0.5 d |
| `core/coretest/harness.go` | option for apps' integration tests (W6 did the same) | < 0.1 d |
| **Core subtotal** | | **≈ 2.3 d** |
| `internal/books/task_start_corequirements.go` (new) | 141 rows + reason constants | 0.3 d |
| `internal/books/options.go` + `task_start_corequirements_test.go` | flip-gated field; C19-C21 | 0.3 d |
| **ABO subtotal** | | **≈ 0.6 d** |
| Later | OpenAPI extension on `startTaskRun` when F2-16 mounts it; one row per new kind | 0.1 d + minutes per kind |

## 11. Risks

1. **B1 has not landed.** The origin-based `SystemStart` is not in code. The design
   survives because it keys on actor and checker. It breaks if B1 lets a
   user-actor start carry a system origin (open question 7).
2. **Inheritance through `rc.Enqueue`.** A user who may start an unfloored parent
   reaches a floored child (lsh-index-build → fingerprint-rescan). This is HEAD
   parity, but it is reach the table does not show. C12 and C20 list every pair.
3. **taskhttp is unmounted.** P1-P3 are unit-level until F2-16. Until then the only
   live enforcement is `permitStart`, for P4 and P5.
4. **Two checks on approve.** The handler pre-check and `permitStart` can drift.
   C7a and C7b pin both.
5. **Completeness couples core to the app.** A core PR that registers a new core
   kind (`core.backup.*`, `core.updater.*` when they register) fails ABO's boot
   until ABO's row lands. This is intended, and it is the same repo, so the same PR
   adds the row.
6. **Window narrowing, if it is ever added.** Section 4.4 keeps windows out.
   Adding them later narrows against HEAD's settings-only window route only if a
   class kind becomes a window member.
7. **Owner-route answer shape.** An ABO owner route that calls `Client.Start` must
   map `*StartCoRequirementError`. Otherwise the floor answers with a generic 403
   (`tasks.actor_not_permitted`).
8. **Class C stays partial.** Reject and View for the 3 Repairs kinds keep the
   native gate (open question 8).
9. **Describe sees module kinds only.** A missing core-kind row fails a real boot,
   not CI's describe. C14c and an ABO unit test (C20) close that.

## 12. Alternatives

- **The hybrid** folds `books:scan` (`books:edit`) into each kind's `Perms.Start` and
  sets `Perms.Approve` to the settings requirement. It changes the field that four
  other rules read:
  - the cancel fallback (`mayCancel`, `control.go:202-205`), which would need a
    `Perms.Cancel` on all 64 kinds as well;
  - both decide fallbacks;
  - the module system actor's `systemPerms` union (`start.go:321-349`), which would
    start holding `books:scan` and change the SEC2-15 surface;
  - the window member check.

  It is also 64 per-kind edits of two or three fields each. It still cannot reach
  the 2 core kinds, because core cannot name `books:*`, so it needs (c) or (b')
  anyway.
- **(b')** re-points the one surviving v2 route. The kinds then disappear from the
  generic API (`can_start` false), core must make its 2 kinds `OwnerRouteOnly`, and
  only that one HTTP path is gated: `ModuleClient` starts and approve are not. No
  completeness check stops the next kind from forgetting.
- **(c)** is one mechanism, outside `Perms`, judged in one function, and complete by
  construction.

## 13. Open questions for the coordinator

1. Should core **require** `TaskStartCoRequirements` from every app that has
   `TasksBoot` (so that `coretest` and `examples` carry rows), or arm completeness
   only when the field is non-nil, as designed, with ABO's non-nil pin?
2. **Windows.** Keep members unjudged on a user's window start (HEAD parity, as
   designed), or judge members' floors in `permitWindow` (fail-closed, and a
   narrowing against HEAD's settings-only window route if a class kind is ever a
   member)?
3. **`can_approve`**: AND the floor (as designed, since accept is a start), even
   though reject stays available without it?
4. Should P7 refuse a floored kind that declares a `Schedule`, because a
   `core:tasks.manage` holder could then run it on a timer as the system actor?
   None does today.
5. **Transitive reach** through `rc.Enqueue` under a user actor (risk 2): accept it
   as HEAD parity, or add a static rule (floor(child) ⊆ floor(parent)) once
   parent → child pairs are declared?
6. Add a `Denied` record for **Start** refusals at `startRun` too (about 0.1 day),
   or only for the floor, as designed?
7. **F5-18 lead:** confirm that B1's origins always carry a module system actor and
   that `Client.Start` can never set one.
8. **Class C:** should the 3 Repairs kinds' reject and View regain `books:edit`?
   That would need an Approve/View co-requirement, which this design does not cover
   ("fresh Start only").
