<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-31-server-start-decomposition.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b6372c5-9f85-417d-badc-16d3949e7f28 -->
<!-- last-edited: 2026-07-03 -->

# TASK-31 — Decompose Server.Start + single lifecycle authority (SYS-2 / SYS-4)

**Priority:** P3 · **Effort:** L · **Recommended subagent:** Opus · **Wave:** 6 · **Depends on:** TASK-19, TASK-22

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-31-server-start-decomposition" -b agent/cr-31-server-start-decomposition origin/main
cd "$REPO/.worktrees/cr-31-server-start-decomposition"
git rebase origin/main
```

**Before doing anything else:** read the merged diffs for TASK-19
(shutdown escape-hatch + `Registry.Shutdown` goroutine-tracking fix) and
TASK-22 (NutsDB retirement) on `origin/main`:

```bash
git log --oneline --all --grep "cr-19-shutdown-escape-hatch\|cr-22-nutsdb-retirement" -20
git log -p --follow -- internal/server/server_lifecycle.go | less   # skim for TASK-19/22 commits
```

Both tasks touch the exact teardown sequence this brief decomposes. If either
is not yet merged, STOP and escalate — do not attempt this refactor against a
moving target.

## Goal

`Server.Start` (in `internal/server/server_lifecycle.go`) is a ~665-line
function mixing container start, cache warmers, HTTP/2/3/TLS setup, role
seeding, v1→v2 operation resume, file watchers, four ticker loops, signal
handling, and a ~175-line hand-sequenced teardown — with **two competing
lifecycle authorities** (inline `Stop` calls vs `container.Stop`) and **four
separate goroutine-tracking mechanisms**. This task:

1. Splits `Start` into named, testable phase methods.
2. Makes the `container` (`internal/serviceregistry`) the **single** lifecycle
   authority — registration order defines teardown order, with no parallel
   hand-maintained Stop sequence relying on idempotence as a safety net.
3. Folds the ad hoc goroutine trackers into the `bgWG` (`namedWaitGroup`)
   pattern already used for embedding/backfill goroutines, eliminating the
   local `backgroundWG` + shutdown-channel duplication where the underlying
   goroutine can instead be tracked via `bgWG` or a container `Stopper`.
4. Adds a startup guard that counts remaining v1-format operation rows before
   `resumeLegacyOp` runs, as a precondition for eventually deleting the shim
   (see SYS-4) — this task does NOT delete `resumeLegacyOp` itself, only adds
   the counting/telemetry gate. Deletion is a separate, later task once the
   count is verified zero in prod over a full release cycle.

This is a **high-risk refactor** on the server's boot/shutdown path. Work in
small, separately-testable commits; keep the full test suite plus any
`-race` lifecycle tests green after every commit, not just at the end.

## Background (verify before editing)

Findings are drawn from `docs/consultancy/01-storage-architecture.md` findings
SYS-2 and SYS-4. Both cite `internal/server/server_lifecycle.go`. Line numbers
in that doc are dated 2026-07-02 and may have drifted further since — always
re-verify with the greps below before editing.

**SYS-2 (dual lifecycle authorities):**

- `func (s *Server) Start(cfg ServerConfig) error` currently starts at
  `server_lifecycle.go:211` and its closing `return nil` / `}` is around
  `:877` (verify — do not trust this number, re-grep as shown below).
- A comment block literally named `SERVER-LIFECYCLE-FLIP` appears twice:
  once near the top of `Start` (around `:214`, describing that
  `container.Start` runs Starter services in resolved dep order) and again
  in the teardown section (around `:799-807`), which admits the split-brain
  directly in its own text: *"Inline Stops above remain the source of truth
  for the carefully-sequenced teardown (opRegistry drain before bgCancel,
  writeBackBatcher flush before itunesSvc.Shutdown, etc.); Container.Stop is
  idempotent on already-stopped services for those."* Two stop orders coexist
  today, relying on idempotence rather than a single declared dependency
  graph.
- Teardown coordinates (at least) four separate concurrency trackers:
  - `s.bgWG` — a `namedWaitGroup` field on `*Server` (declared in
    `internal/server/server.go`, used throughout `Start` for background
    goroutines such as `index-worker`, `external-id-backfill`,
    `acoustid-backfill`, `versiongroup-backfill`, `strip-movement-atoms`,
    `remux-malformed-m4b`, `build-search-index`, `transcode+quarantine`).
  - A function-local `var backgroundWG sync.WaitGroup` declared inside
    `Start` itself (around `:525`), used for the scheduler, ticker loops, and
    signal-handling goroutine, coordinated with a local `shutdown` channel.
  - The operation registry's own internal `goroutineWG` (in
    `internal/operations/registry/registry.go`), which tracks the
    dispatcher/watchdog/worker/dep-notify goroutines and is drained inside
    the registry's own `Shutdown`, a *different* mechanism than either of the
    above two.
  - `container.Stop` (`internal/serviceregistry/container.go:214`), which
    walks registered `Stopper`s in reverse-resolved order.
  - Every new background task added to `Start` must pick the *correct* one of
    these four, or it silently escapes shutdown tracking — this is exactly
    the class of bug TASK-19 fixed once (`Registry.Shutdown` goroutine-tracking,
    SYS-1/BUG-2). Read that PR's diff (see START HERE) to avoid regressing it.

**SYS-4 (v1→v2 legacy resume shim):**

- `func (s *Server) resumeLegacyOp(opID, opType string)` (around `:111-206`)
  hardcodes a `switch` over ~10 pre-UOS v1 operation type name strings
  (`"itunes_import"`, `"scan"`, `"organize"`, `"bulk_write_back"`,
  `"isbn-enrichment"`, `"metadata-refresh"`, `"itunes_path_reconcile"`,
  `"itunes_path_repair"`, `"transcode"`, `"diagnostics_export"`,
  `"diagnostics_ai"`, `"itunes_sync"`, `"reconcile_scan"`), each with
  near-identical re-enqueue-via-registry boilerplate, plus a `default` branch
  that falls back to `maintenance.job` resume. This must be kept until no
  production DB can contain a v1-format operation row — this task must NOT
  delete it. It only adds a startup count/telemetry gate as a precondition
  for a future deletion task.
- The v1 `OperationQueue` itself is already gone from
  `internal/operations/` (no `queue.go`, zero non-test `GlobalQueue`
  references in production code) — confirm this is still true with the grep
  below before writing the count-gate, since if v1 rows genuinely cannot
  exist anymore the count will always be zero and the gate is trivial
  (which is fine — the point is to make that fact *observable*, not to
  assume it).

**Re-verify these anchors before editing** — line numbers drift:

```bash
grep -n "^func (s \*Server) Start\|^func (s \*Server) resumeLegacyOp\|^func (s \*Server) resumeInterruptedOperations\|SERVER-LIFECYCLE-FLIP\|var backgroundWG\|s\.bgWG\.\|container\.Stop(" internal/server/server_lifecycle.go
grep -n "bgWG\s\+namedWaitGroup\|type namedWaitGroup" internal/server/server.go internal/server/*.go
grep -n "goroutineWG" internal/operations/registry/registry.go
grep -n "^func (c \*Container) Stop\|^type Stopper interface" internal/serviceregistry/container.go internal/serviceregistry/lifecycle.go
grep -rn "GlobalQueue" --include='*.go' internal/ | grep -v _test.go
```

Confirm the shape of the teardown section (should show the
`SERVER-LIFECYCLE-FLIP` comment immediately before `s.container.Stop(...)`,
following a long sequence of hand-ordered inline Stop calls):

```bash
grep -n "opRegistry\|writeBackBatcher\|itunesSvc\|hnswPersistDir\|embedQueue\|ollamaDaemon\|embeddingStore\|aiScanStore" internal/server/server_lifecycle.go
```

## Step-by-step

1. **Read TASK-19 and TASK-22's merged diffs first** (see START HERE). Note
   any teardown-order changes they made so this refactor builds on the
   current state, not a stale mental model.

2. **Map the current teardown order exactly**, top to bottom, from the
   inline Stop sequence through `s.container.Stop(...)` to the final
   `backgroundWG.Wait()`. Write this order down (as a code comment or a
   scratch note) before changing anything — this is the invariant you must
   preserve unless you are deliberately fixing an ordering bug found in
   TASK-19/22.

3. **Extract `Start` into named phase methods** on `*Server`, each taking
   `cfg ServerConfig` or narrower params as needed, called in sequence from a
   slimmed-down `Start`. Suggested split (adjust names/boundaries to match
   what the actual code supports — do not force an unnatural boundary):
   - `storesUp(cfg) error` — container start, store/index wiring
     (the `indexedStore` wrap section).
   - `resumeOps()` — the `resumeInterruptedOperations` / `resumeLegacyOp` call
     site (already a separate function; just make sure `Start` calls it
     explicitly as a named phase rather than inline).
   - `backfillsUp()` — the `bgWG`-tracked backfill goroutines
     (external-id, acoustid, versiongroup, strip-movement-atoms,
     remux-malformed-m4b, build-search-index, transcode+quarantine).
   - `registryUp()` / `schedulerUp()` — scheduler + ticker loop goroutines
     currently tracked by the local `backgroundWG`.
   - `httpUp(cfg) error` — HTTP/2/3/TLS listener setup and signal handling.
   - `shutdown(ctx) error` (or keep as an existing method if one already
     exists under a different name — grep for it) — the teardown sequence.
   Each phase method should be independently unit-testable (construct a
   minimal `*Server`, call the phase, assert on its side effects) where the
   existing test harness supports it — do not invent a new test harness from
   scratch if `internal/server/server_more_test.go` or similar already has
   one; extend it.

4. **Single lifecycle authority**: for every service currently stopped by an
   inline hand-ordered call in the teardown section AND already registered
   as a `Stopper` in the container (or straightforward to register as one —
   e.g. `opRegistry`, `writeBackBatcher`, `itunesSvc`, HNSW export,
   `embedQueue`/`ollamaDaemon`, `embeddingStore`/`aiScanStore` closes), move
   the Stop logic into a `Stopper` implementation registered with the
   container at the correct dependency position, and delete the
   corresponding inline call. Registration order in the container becomes
   the sole source of teardown order — the `SERVER-LIFECYCLE-FLIP` comments
   (both occurrences) should be deleted once their split-brain is resolved,
   not left as historical documentation of a fixed bug.
   - If a service's stop sequencing genuinely cannot be expressed as a
     simple reverse-dep-order `Stopper` (e.g. it must happen strictly
     between two other steps that aren't dependencies of each other), do NOT
     force it — leave it inline, add a comment explaining why, and note it in
     the PR description as a known remaining exception. Do not invent a
     dependency-graph feature in `serviceregistry` for this task; that's a
     larger change.

5. **Fold goroutine trackers**: replace the function-local `backgroundWG` +
   shutdown-channel pattern with `s.bgWG` where the tracked goroutine can be
   named and drained the same way the existing backfill goroutines are
   (`s.bgWG.Add("name")` / `defer s.bgWG.Done("name")`). Do not touch the
   operation registry's internal `goroutineWG` — that is a different
   package's internal concern and already fixed by TASK-19; only reference it
   here as one of the "four trackers" to explain in comments why it is
   intentionally left alone.

6. **v1 resume-shim count gate (SYS-4)**: add a small startup check —
   inside or adjacent to `resumeInterruptedOperations` — that counts
   operations whose `Type` matches one of the hardcoded v1 legacy names in
   `resumeLegacyOp`'s switch, and logs the count at `slog.Info` (e.g.
   `slog.Info("legacy v1 op rows pending resume", "count", n)`) or exposes it
   as a metric if the codebase already has a metrics-emission convention for
   startup counts (grep for an existing pattern before inventing one). Do
   **not** delete `resumeLegacyOp` or change its behavior — this is
   observability only, laying groundwork for a future deletion task gated on
   the count staying at zero across a release cycle.

7. Bump the file header (version bump + `last-edited` date) on every file you
   touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
cd internal/server && go build ./...
go vet ./internal/server/... ./internal/serviceregistry/... ./internal/operations/...
go test ./internal/server/... -count=1 -race
go test ./internal/serviceregistry/... -count=1 -race
go test ./internal/operations/... -count=1 -race
go build ./...
```

Run the full targeted suite after **every** commit in this refactor, not
just once at the end — a shutdown-ordering regression is exactly the kind of
bug that passes a quick `go build` and fails only under `-race` or in a real
restart.

## Acceptance criteria

- [ ] `Start` is decomposed into named phase methods; no single method in
      `server_lifecycle.go` handling boot mixes container start, HTTP setup,
      backfills, and teardown in one ~600+ line body.
- [ ] Both `SERVER-LIFECYCLE-FLIP` comments are gone, replaced by a single
      teardown authority (the container's registered `Stopper`s in
      reverse-resolved order), OR — if some services genuinely cannot be
      expressed as `Stopper`s — the remaining inline exceptions are
      explicitly documented with a reason in a PR-description list.
  - [ ] No `Stop` call for a container-registered service is duplicated
      inline "just in case" — idempotence is no longer relied upon as the
      safety net for ordering.
- [ ] The function-local `backgroundWG` + shutdown-channel pattern is folded
      into `s.bgWG` wherever the tracked goroutine can be named/drained the
      same way as existing backfills, OR left in place with a comment
      explaining why it couldn't be folded.
- [ ] `resumeLegacyOp` is untouched behaviorally; a new startup count/log of
      pending v1-format op rows exists and is verifiable in test.
- [ ] `go build ./...`, `go vet` on the three packages above, and
      `go test ./internal/server/... ./internal/serviceregistry/...
      ./internal/operations/... -count=1 -race` are all green.
- [ ] File headers bumped on every changed file.
- [ ] PR description explicitly lists: (a) any Stoppers newly registered,
      (b) any inline Stop exceptions kept and why, (c) the count-gate log
      line added for SYS-4.

## Commit message

```
refactor(server): decompose Server.Start into phases, single lifecycle authority (SYS-2/SYS-4)

Server.Start mixed container start, HTTP setup, background backfills, and a
hand-sequenced ~175-line teardown in one ~665-line function with two
competing lifecycle authorities (inline Stops vs container.Stop) relying on
idempotence instead of a declared dependency graph, plus four separate
goroutine-tracking mechanisms. Split into named phase methods, make the
container the single source of teardown order, fold the local backgroundWG
into bgWG where possible, and add an observability gate counting remaining
v1-format legacy op rows ahead of a future resumeLegacyOp removal.

Co-Authored-By: Claude Opus <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-31-server-start-decomposition
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Before starting, re-check whether this work is already done or partially
done:

```bash
grep -n "SERVER-LIFECYCLE-FLIP" internal/server/server_lifecycle.go
```

- If this grep returns **no matches**, the split-brain comment is already
  gone — check whether a prior PR already did this decomposition (`git log
  --oneline -- internal/server/server_lifecycle.go`) before redoing the work.
- If `Start`'s body is already under ~150 lines with clearly named phase
  calls, this task is substantially complete — verify against the acceptance
  criteria above and only fill genuine gaps (e.g. the SYS-4 count gate may
  still be missing even if the SYS-2 decomposition is done).
- If TASK-19 or TASK-22 changed the teardown sequence significantly since
  this brief was written, prefer their current code shape over anything
  described here — the anchors and step order in this brief are illustrative
  of the pattern to apply, not a literal diff to replay.

**Rollback:** revert the commit(s). This refactor is behavior-preserving by
design (no new external-facing functionality) — a clean revert should be
sufficient. If the count-gate log line was picked up by an external
dashboard/alert before rollback, note that in the revert PR description.
