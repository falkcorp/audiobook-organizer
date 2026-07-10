<!-- file: docs/agent-tasks/dedup-pipeline-hardening/TASK-05-emit-mutex-sharding.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b095845-8df6-4e37-9f88-7085ebc88bb9 -->
<!-- last-edited: 2026-07-10 -->

# TASK-05 — CONC-3: shard the full-scan emit() off its single global mutex; -race proof (INIT-2 T5) [⚠ review-critical]

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). EXCEPTIONS: T3's 387k-backlog drain and T6's CONS-10 prod drain are prod-data mutations -> dry-run FIRST, then a real AskUserQuestion apply gate.
**File-ownership:** INIT-2 OWNS all structural edits to `internal/dedup/engine.go` — this task is that owner. WAVE ORDER: TASK-03 edits the same file and merges FIRST; do not start until TASK-03's PR is merged and this worktree is rebased on it. Never run concurrently with any INIT-1 engine.go wave.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Opus/strong-class · concurrency-invariant subagent · **Why:** lock-sharding must preserve per-pair check-then-set atomicity — a subtle race double-emits or corrupts a 44k-book prod scan (CONC-4's latent MergeBooks race is the precedent) · **Depends on:** TASK-03

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-pipeline-hardening-emit-mutex-sharding" -b agent/dedup-pipeline-hardening-emit-mutex-sharding origin/main
cd "$REPO/.worktrees/dedup-pipeline-hardening-emit-mutex-sharding"
git rebase origin/main
# Precondition: TASK-03 must already be merged. Verify (>=1 hit, else STOP):
grep -n "dedup_stale_drain_v2_done" internal/plugins/dedup/drain_stale.go
```

## Goal

Remove the CONC-3 hotspot: the full-scan `emit()` closure in `internal/dedup/engine.go` runs
its ENTIRE body under one `sync.Mutex` guarding four maps + a counter, with fallback
`GetBookByID`/`GetBookFiles` store reads UNDER the lock — so the surrounding
`registry.RunItems` NumCPU worker pool serializes behind one mutex and one straggling store
read stalls every worker. Shard it: per-pair state (`emitted`) moves to shard-selected
mutexes keyed by pair-key hash (same pair ⇒ same shard ⇒ the check-then-set atomicity the
in-code CONC-3 comment mandates is preserved per pair); per-book caches leave the emit lock;
the counter goes atomic. The `registry.RunItems` pool itself is UNTOUCHED — this task fixes
the lock, not the loop (the parallel-sibling pattern, `internal/plugins/acoustid/backfill.go`,
is already in place here). Pebble NoSync lesson (PR #1855): candidate upserts already commit
NoSync — do NOT introduce any lock held across a store write; `UpsertCandidate` must be
called OUTSIDE all shard locks or with only the pair's own shard held, never a global one.

## Background (verify before editing)

- The CONC-3 comment block sits directly above `var mu sync.Mutex` in the full-scan section
  of `engine.go` and names the shared state: `booksByID`, `boilerplateBookCache`,
  `parentDirCache`, `emitted` (maps) + `identifierGateDrops` (counter). It also documents WHY
  the whole emit() body is currently under one lock: the emitted-key check-then-set must be
  atomic per pair. Your sharding must preserve exactly that invariant — per PAIR, not
  globally.
- Helpers `isBoilerplateBook`, `parentDirForBook`, `bookForIdentifierGate` are documented as
  "only called from within the locked emit() body — mu is already held". After your change
  each becomes safe under its OWN synchronization (see steps) and those comments MUST be
  updated to match reality.
- The books slice is fully known before the pool starts: `booksByID` and
  `boilerplateBookCache` are ALREADY pre-seeded from it. The under-lock lazy fills exist only
  for LSH-returned books outside the scan slice — that fallback path stays, but behind its
  own small mutex (or `sync.Map`), never inside a pair-shard lock.
- Nil/unknown semantics (spell-out): a book the fallback lookup cannot fetch (`err != nil` or
  nil) must behave exactly as today — `bookForIdentifierGate` returns nil and
  `identifiersConflict(nil, x)` is false (conservative, never blocks emission);
  `parentDirForBook` returns "" (unknown) and unknown-dir never suppresses. Do not flip these.
- Do NOT touch: the guard chain in `upsertExactCandidate` (TASK-03's, already merged), the
  LSH/segment-walk code outside the emit section, `embedding_store.go`, or the
  `registry.RunItems` invocation and its options.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  # Edit target: the single mutex + CONC-3 comment (~engine.go:3683, 1 hit)
  grep -n 'var mu sync.Mutex' internal/dedup/engine.go
  # Edit targets: the emit closure + helpers (>=4 hits)
  grep -n 'emit := func(bookAID, bookBID string\|isBoilerplateBook := func\|parentDirForBook := func\|bookForIdentifierGate := func\|identifierGateDrops' internal/dedup/engine.go
  # Untouched context: the pool (>=1 hit near the emit section)
  grep -n 'registry.RunItems' internal/dedup/engine.go
  # Copy-from source: existing parallel test shape (1 file)
  ls internal/dedup/engine_fullscan_layer1_parallel_test.go
  ```
  Zero hits on any edit-target grep at execution time = STOP and report.

## Step-by-step

1. Run the anchor greps; read the CONC-3 comment block in full before changing anything.
2. Introduce a small shard type in the same section (keep it function-local or unexported
   package-level next to the scan — do not create a new file for it):
   `emitShards [16]struct { mu sync.Mutex; emitted map[string]struct{} }` with
   `shardFor(pairKey string) *shard` via FNV-1a hash. Reuse the existing `pairKey`
   canonicalization closure — do not re-derive key format.
3. Rewrite `emit()`: compute `key := pairKey(a, b)`; lock ONLY `shardFor(key)`; do the
   emitted check-then-set + the gate calls that need per-pair atomicity inside that shard
   lock; release before calling the store upsert if the current code structure allows marking
   `emitted[key]` first (mark-before-upsert is the existing semantics — a marked pair is
   never retried; keep that exact semantics).
4. Move per-book state out of the pair lock: `booksByID` + `boilerplateBookCache` +
   `parentDirCache` get ONE dedicated `sync.Mutex` (bookCacheMu) used only inside the three
   helper fallbacks; the helpers' store reads (`GetBookByID`/`GetBookFiles`) execute OUTSIDE
   any pair-shard lock (double-checked: read cache under bookCacheMu, miss ⇒ unlock ⇒ store
   read ⇒ relock ⇒ recheck-then-store). A duplicate concurrent fetch of the same book is
   acceptable (idempotent read); a store read under a pair-shard lock is NOT.
5. `identifierGateDrops` becomes `atomic.Int64`; the final log/read site updates accordingly.
6. Update the CONC-3 comment block and the three helper comments to describe the new
   sharding + bookCacheMu scheme (they currently assert single-mutex facts that become lies).
7. Purely additive/behavior-preserving elsewhere: no signature changes, no changes to what
   gets emitted (only to locking), no touching TASK-03's guard code.
8. NEW `internal/dedup/engine_emit_shard_race_test.go` (mirror the fixture/setup shape of
   `engine_fullscan_layer1_parallel_test.go`): drive the scan (or the emit closure directly)
   with ≥4 goroutines over (i) many emissions of the SAME pair (assert exactly one stored
   candidate — per-pair atomicity survives), and (ii) many distinct pairs (assert count ==
   serial expectation — no lost emissions; this is the anti-over-suppression case: sharding
   must not silently drop emissions). Must run under `-race`.
9. Bump the file header (version + last-edited) on every file you touch; keep existing guids.
10. Run the gate INCLUDING the explicit race run below.

## How to test

```bash
make ci
go test -race -run 'TestEmitShard|FullScan' ./internal/dedup/... -short
```

Caveat (verbatim): staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck
to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "emitShards" internal/dedup/engine.go` hits, and `grep -n 'var mu sync.Mutex' internal/dedup/engine.go` no longer matches inside the full-scan emit section (the old single mutex there is gone; other functions' locals may still match elsewhere — check context)
- [ ] No `GetBookByID`/`GetBookFiles` call executes under a pair-shard lock (reviewer line-checks the helper bodies)
- [ ] `-race` test green: same-pair flood yields exactly 1 stored candidate; distinct-pair flood yields the full serial count (no lost emissions — anti-over-suppression)
- [ ] Nil-book fallback and unknown-dir semantics unchanged (conservative, non-suppressing — asserted in the race test fixture)
- [ ] Tests green; vet/lint clean on changed files (`make ci` exits 0).
- [ ] File headers bumped on every changed file (`grep -n "last-edited: " <file>` shows 2026-07-10 or later).

## Commit message

```
perf(dedup): shard full-scan emit() mutex; move book lookups off the pair lock (INIT-2 T5, CONC-3)

Per-pair check-then-set atomicity preserved by hashing the canonical pair
key to one of 16 shard mutexes (same pair -> same shard); per-book caches
get a dedicated mutex with store reads outside all pair locks;
identifierGateDrops goes atomic. RunItems pool untouched. -race test
proves no double-emit and no lost emissions.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-pipeline-hardening-emit-mutex-sharding
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "emitShards" internal/dedup/engine.go` hits AND the full-scan emit section no longer declares its own `var mu sync.Mutex` (old-location absence — verify by reading the CONC-3 comment region), this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the single-mutex emit() is restored exactly (behavior identical, just slower); no persisted state involved.
