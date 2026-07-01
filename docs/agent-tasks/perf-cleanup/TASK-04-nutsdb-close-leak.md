<!-- file: docs/agent-tasks/perf-cleanup/TASK-04-nutsdb-close-leak.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3e2a8d61-9b74-4c05-8e12-6f4d1a2c7b90 -->
<!-- last-edited: 2026-07-01 -->

# TASK-04 — Investigate/document NutsActivityStore.Close() goroutine note (NUTSDB-CLOSE, OPTIONAL)

**Priority:** P4 (lowest in this workstream) · **Effort:** XS · **Recommended subagent:** Haiku · doc/investigation subagent · **Depends on:** none

> ⚠️ **OPTIONAL / LIKELY DOCUMENTATION-ONLY.** This task's original evidence
> ("a background goroutine is not signalled to stop; add a stop channel") does
> **not match the actual code** — see Background below. Read the full
> Background section before writing any code. Do **not** invent a stop
> channel or context-cancel mechanism for `NutsActivityStore.Close()` — there
> is nothing in `nuts_activity_store.go` that owns a goroutine to signal.
> If your investigation confirms the analysis below, the correct deliverable
> is a **documentation-only PR** (a code comment + this task's notes), not a
> code fix. Do not fork or upgrade the `nutsdb` dependency — `TODO.md`
> explicitly forbids this (see below).

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-nutsdb-close-leak" -b agent/pc-nutsdb-close-leak origin/main
cd "$REPO/.worktrees/pc-nutsdb-close-leak"
git rebase origin/main
```

## Goal

Investigate the `NUTSDB-CLOSE-GOROUTINE-LEAK` TODO entry, confirm what (if
anything) is actually leaking and where, and either (a) add a short doc
comment above `NutsActivityStore.Close()` recording the finding + linking
back to the TODO decision so a future contributor doesn't re-open this as a
"bug", or (b) if you find something genuinely actionable in **our own code**
(not the vendored `nutsdb` library), fix it narrowly. Given the investigation
already done for this brief (below), (a) is the expected outcome.

## Background (verify before editing — this is the important part)

Re-run these first:
```bash
grep -n "NUTSDB-CLOSE-GOROUTINE-LEAK" TODO.md
sed -n '869,877p' TODO.md
grep -n "func (s \*NutsActivityStore) Close\|go func(" internal/database/nuts_activity_store.go
```

The actual `TODO.md` entry (as of authoring) reads:

> **NUTSDB-CLOSE-GOROUTINE-LEAK** (2026-06-17, low priority) —
> `NutsActivityStore.Close()` → `nutsdb.DB.Close()` leaks **1 background
> goroutine per Open** (the TTL time-wheel; isolation micro-test: 20
> open+close cycles → 20 survivors). **Benign in prod** — the activity store
> is a process-lifetime singleton (one open at startup,
> `internal/activity/register.go`). Only the test suite (which opens many
> short-lived stores) accumulates them. **Do NOT fork/upgrade nutsdb to chase
> this** — `nuts_activity_store.go` is coupled to v1.1.0-specific error
> sentinels (`ErrNotFoundBucket` vs `ErrBucketNotFound`) and an upgrade risks
> breaking empty-scan handling. If it ever matters, add an option to skip the
> TTL manager, or share one store across the server test package.

Key facts confirmed by reading the code (re-verify these yourself with the
grep commands above and by reading the vendored source under
`$(go env GOMODCACHE)/github.com/nutsdb/nutsdb@v1.1.0/` and
`$(go env GOMODCACHE)/github.com/antlabs/timer@*/`):

1. `internal/database/nuts_activity_store.go`'s `Close()` is a **one-line
   passthrough**: `func (s *NutsActivityStore) Close() error { return
   s.db.Close() }`. There is **no goroutine started anywhere in
   `nuts_activity_store.go` itself** — `grep -n "go func(" internal/database/nuts_activity_store.go` returns nothing.
2. The goroutine that leaks is started **inside the vendored `nutsdb`
   library**, not our code: `nutsdb.Open()` calls `go db.tm.run()`, which
   runs a TTL time-wheel (`github.com/antlabs/timer`)'s internal ticker
   goroutine.
3. `nutsdb.DB.Close()` (which our `Close()` already calls) **does** attempt
   to stop this: it calls `db.tm.close()`, which calls `tm.t.Stop()` on the
   time-wheel. Whether that fully reaps the underlying goroutine
   immediately, or just stops future ticks (leaving the goroutine parked
   until GC/process exit), is a **third-party library implementation
   detail** we do not control and should not patch around by vendoring or
   forking (the TODO explicitly forbids this).
4. `NutsActivityStore` never writes with a TTL/expiry (`grep -n "tx.Put("
   internal/database/nuts_activity_store.go` — every call passes `0` for the
   TTL argument), so the TTL manager goroutine is running unconditionally for
   a feature this store never uses, but `nutsdb.Options` has **no field to
   disable the TTL manager at Open time** (checked
   `$(go env GOMODCACHE)/github.com/nutsdb/nutsdb@v1.1.0/options.go` — no such
   option exists in v1.1.0). Adding one would mean patching the vendored
   library, which the TODO forbids.
5. The two "if it ever matters" mitigations the TODO suggests are: (a) an
   upstream nutsdb option to skip the TTL manager (does not exist in v1.1.0 —
   see #4), or (b) share one store across the server test package. Given (a)
   is unavailable without forking, only (b) is a real option, and it is a
   **test-only** change with its own tradeoffs (test isolation — each test
   currently uses its own `t.TempDir()`; sharing a store across tests in a
   package means shared on-disk state and potential cross-test interference,
   which is a bigger change than this task's XS effort budget covers).

**Conclusion the worker should independently verify, not just copy:** there
is nothing to fix in `internal/database/nuts_activity_store.go`'s `Close()`
itself — it already correctly delegates to the library's own close/cleanup
path. The right XS-effort deliverable is a code comment recording this so the
TODO doesn't get mis-read as "our bug" again, plus (optionally) a narrow
regression test guarding that `Close()` keeps delegating to `db.Close()`
(protects against a future refactor accidentally dropping the delegation, not
against the third-party leak itself).

## Step-by-step

1. Run all the grep/investigation commands above yourself and confirm the
   findings still hold (line numbers, library version, `options.go`
   contents). If something has changed (e.g. nutsdb version bumped, or a new
   `SkipTTLManager`-style option now exists), stop and re-scope: implement
   the now-available option instead of the doc-only path, and update this
   brief's checklist accordingly in your PR description.
2. If the findings hold (expected): add a short doc comment directly above
   `func (s *NutsActivityStore) Close() error` in
   `internal/database/nuts_activity_store.go`, e.g.:
   ```go
   // Close shuts down the underlying NutsDB handle. Note: nutsdb v1.1.0
   // starts a background TTL time-wheel goroutine on Open() regardless of
   // whether TTL/expiry is ever used (it is not, here — see the tx.Put(...,
   // 0) calls throughout this file). db.Close() already calls tm.close()
   // internally to stop it; there is no additional goroutine owned by this
   // file to signal. Benign in prod (process-lifetime singleton, see
   // internal/activity/register.go) — only accumulates in short-lived test
   // opens. See TODO.md NUTSDB-CLOSE-GOROUTINE-LEAK. Do NOT fork/upgrade
   // nutsdb to chase this further.
   func (s *NutsActivityStore) Close() error { return s.db.Close() }
   ```
3. Optionally add a narrow regression test in
   `internal/database/nuts_activity_store_test.go` (check if such a file
   exists first) asserting `Close()` returns `nil` for a freshly opened store
   and that a second `Close()` call does not panic (guards the delegation
   itself, not the goroutine count — do **not** write a `runtime.NumGoroutine()`-based
   assertion; it will be flaky given the third-party library's async
   teardown timing).
4. Update the `TODO.md` entry for `NUTSDB-CLOSE-GOROUTINE-LEAK` to mark it as
   investigated/documented (not "fixed" — it remains an accepted, benign,
   third-party limitation). Do not delete the entry.
5. Bump the file header on `internal/database/nuts_activity_store.go`.

## How to test

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/pc-nutsdb-close-leak
go build ./...
go test ./internal/database/... -run TestNutsActivityStore -v -count=1
go test ./internal/database/... -count=1
go vet ./internal/database/...
```

## Acceptance criteria
- [ ] Investigation re-verified against current code/vendored library (not just copied from this brief).
- [ ] If findings hold: a doc comment is added above `NutsActivityStore.Close()` explaining the situation and linking to the TODO entry; **no fabricated stop-channel/context-cancel code is added**.
- [ ] If findings do NOT hold (e.g. a real fixable leak in our own code is found, or nutsdb now exposes a TTL-disable option): the fix is narrow, touches only our code (never forks/vendors nutsdb), and this deviation is called out clearly in the PR description.
- [ ] `TODO.md`'s `NUTSDB-CLOSE-GOROUTINE-LEAK` entry is updated to reflect the investigation outcome (not deleted).
- [ ] `go build`, `go test ./internal/database/...`, `go vet` all green.
- [ ] File headers bumped.

## Commit message
```
docs(database): record NutsActivityStore.Close() TTL-goroutine investigation (NUTSDB-CLOSE)

Confirms the TTL time-wheel goroutine started by nutsdb.Open() is already
stopped via db.Close() -> tm.close(); there is no goroutine owned by our own
Close() to signal. Documents this inline so the TODO isn't re-opened as a
fixable bug in our code. No behavior change.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/pc-nutsdb-close-leak
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n "NUTSDB-CLOSE-GOROUTINE-LEAK" internal/database/nuts_activity_store.go` — if the doc comment referencing it is already present above `Close()`, this task is done. Rollback: revert the commit; this is a comment/doc-only change with zero behavioral risk.
