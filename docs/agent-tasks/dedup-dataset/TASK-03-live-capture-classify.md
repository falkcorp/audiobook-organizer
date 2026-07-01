&lt;!-- file: docs/agent-tasks/dedup-dataset/TASK-03-live-capture-classify.md --&gt;
&lt;!-- version: 1.0.0 --&gt;
&lt;!-- guid: 558805c7-15ea-4738-b815-ccb3e29a4636 --&gt;
&lt;!-- last-edited: 2026-07-01 --&gt;

# TASK-03 — Wire BuildExample + Classify into the candidate-upsert path (C5)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** TASK-01 (C5-sig) and TASK-02 (C5-folder) must both be merged to `origin/main` first — DO NOT start this task until both are merged.

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dd-live-capture-classify" -b agent/dd-live-capture-classify origin/main
cd "$REPO/.worktrees/dd-live-capture-classify"
git rebase origin/main
```
Do all work inside that worktree. Never edit `main` or the primary checkout.
**Before starting:** confirm TASK-01 and TASK-02 are already on `origin/main`
(`git log --oneline --grep="C5-sig" origin/main` and `--grep="C5-folder"`) — this
task relies on the final `signatureRelation`/`folderRelation` outputs.
**If a separate "dedup-hardening" workstream is also touching
`internal/dedup/engine.go` concurrently, rebase onto `origin/main` again
immediately before you start editing AND immediately before opening the PR** —
this file is a known collision point.

## Goal

`dataset.BuildExample` (builds a `LabeledExample` snapshot from a
`DedupCandidate`) and `dataset.Classify` (applies rule-based auto-labeling) are
currently invoked from only two places: the human-capture handler
(`internal/server/handlers/dedup/label_capture.go:59`) and the batch
`internal/plugins/dedup/dataset_backfill.go` op. They are **not** invoked when a
candidate is first created via the live dedup engine
(`internal/database/embedding_store.go` `UpsertCandidate`, called from many
sites in `internal/dedup/engine.go`). Wire live-capture so every newly-created
candidate also gets a `BuildExample` + `Classify` snapshot recorded
automatically, with an auto `label_source` (not `human`), so the dataset grows
continuously instead of only via manual capture or periodic backfill.

## Background (verify before editing — line numbers drift)

- `internal/database/embedding_store.go`, function `UpsertCandidate` (around
  line 466 as of this writing). It is called from many sites in
  `internal/dedup/engine.go` (grep shows calls around lines 664, 1273, 1537,
  1601, 3023, 3220 as of this writing — confirm current line numbers).
- `internal/dedup/dataset/builder.go`, function
  `BuildExample(store BuilderStore, cand database.DedupCandidate) (database.LabeledExample, error)`
  (around line 35).
- `internal/dedup/dataset/rules.go`, function
  `Classify(ex database.LabeledExample) (label, reason string, fires bool)`
  (around line 34).
- Reference implementation of the existing wiring:
  `internal/server/handlers/dedup/label_capture.go` — read the whole function
  around line 59 to see the exact `BuildExample` → `Classify` → persist
  sequence, including the `builderStoreAdapter` type it uses to satisfy
  `dataset.BuilderStore`.
- `internal/database/dedup_label.go` — check the `LabelSource` field / constants
  (`rule|itunes_attr|human|llm_judge` per the struct comment around line 68) to
  pick the correct auto value (likely `rule`, since `Classify` is rule-based —
  confirm by reading `Classify`'s doc comment).
- The dataset backfill op for reference on how it avoids double-writing
  existing examples: `internal/plugins/dedup/dataset_backfill.go`.

Run these to confirm the current state before editing:
```bash
grep -n "func (s \*EmbeddingStore) UpsertCandidate" internal/database/embedding_store.go
grep -n "UpsertCandidate(database.DedupCandidate{" internal/dedup/engine.go
grep -n "func BuildExample" internal/dedup/dataset/builder.go
grep -n "func Classify" internal/dedup/dataset/rules.go
sed -n '1,80p' internal/server/handlers/dedup/label_capture.go
grep -n "LabelSource" internal/database/dedup_label.go
grep -rn "dataset.BuildExample\|dataset.Classify" internal/plugins/dedup/dataset_backfill.go
```

## Step-by-step

1. Decide where the wiring belongs: prefer doing it **inside
   `EmbeddingStore.UpsertCandidate`** only for the "new pair" branch (the
   `err == pebble.ErrNotFound` branch that assigns a fresh sequential ID) —
   this guarantees it runs exactly once per candidate and avoids re-labeling on
   every score update. If `UpsertCandidate` does not have access to the full
   `*database.Book` objects needed by `BuildExample`'s `BuilderStore` interface,
   instead add the wiring as a small wrapper function in `internal/dedup/engine.go`
   (e.g. `upsertCandidateWithLiveLabel`) that: (a) checks whether the pair is new
   by calling the existing lookup logic or by checking `UpsertCandidate`'s return/
   a new bool return value, (b) calls `dataset.BuildExample` + `dataset.Classify`,
   (c) persists the resulting `LabeledExample` via whatever store method
   `label_capture.go` uses (find it by reading that file fully). Pick whichever
   approach requires the smaller diff given what's actually in scope at each
   call site — confirm by reading both files before deciding.
2. Implement a `BuilderStore` adapter for the engine's DB handle if one does not
   already exist in `internal/dedup/engine.go` (check first — `label_capture.go`
   has `builderStoreAdapter`; reuse it or an equivalent if the engine already
   has access to the same store type. Do not duplicate an identical adapter
   type in two packages — export it from wherever it currently lives if needed,
   or move it to a shared location such as `internal/dedup/dataset` if that
   avoids duplication and doesn't create an import cycle).
3. Set `LabelSource` to the auto value used by rule-based classification
   (confirm the exact string from `Classify`'s doc comment / existing usage in
   `label_capture.go` — do NOT invent a new string).
4. Guard against double-writes: only build+classify+persist when the candidate
   is genuinely new (the `pebble.ErrNotFound` / "new pair" branch), never on an
   update to an existing candidate. Skip silently (log at debug level, do not
   fail the upsert) if `BuildExample` returns an error — a live-capture
   failure must never block the primary candidate upsert.
5. Keep the live-capture write on the same batch/transaction as the candidate
   upsert if `UpsertCandidate` already uses a `pebble.Batch` (it does — see
   `b := s.db.NewBatch()`), so the two writes are atomic. If your chosen
   implementation location doesn't have access to that batch, document why and
   fall back to a best-effort separate write, clearly commented as such.
6. Bump the file header `version` and `last-edited` on every file you touch.

## How to test

Add a test in `internal/database/embedding_store_test.go` (or
`internal/dedup/engine_test.go`, whichever contains the actual call site you
modified) that: creates a new candidate via the modified path, then verifies
via `ListLabeledExamples` (or the direct store lookup used by
`label_capture.go`) that exactly one labeled example now exists for that pair
with the expected `label_source`. Add a second test asserting that calling the
upsert path again for the *same* pair (an update, not a new pair) does NOT
create a second labeled example.

```bash
go build ./...
go test ./internal/dedup/... ./internal/database/... -count=1
go vet ./internal/dedup/... ./internal/database/...
```

## Acceptance criteria

- [ ] Every newly-created dedup candidate (new pair, not an update) triggers a
      `BuildExample` + `Classify` snapshot with an auto `label_source`.
- [ ] Updates to an existing candidate pair do NOT create a duplicate labeled
      example.
- [ ] A `BuildExample`/`Classify` failure never blocks or fails the underlying
      candidate upsert (logged and swallowed).
- [ ] New tests cover both the new-pair-creates-example case and the
      update-does-not-duplicate case, and pass.
- [ ] `go test ./internal/dedup/... ./internal/database/... -count=1` passes;
      `go vet` clean on both packages.
- [ ] File headers bumped on every changed file.

## Commit message
```
feat(dedup): wire BuildExample+Classify into live candidate upsert (C5)

BuildExample/Classify were only invoked from human-capture and the batch
dataset_backfill op, so the labeled dataset only grew via manual review or
periodic backfill. Auto-build and classify a labeled example (auto
label_source) whenever a new dedup candidate pair is created, guarded so
updates to existing pairs never double-write.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/dd-live-capture-classify
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Idempotency: if `UpsertCandidate` (or its engine.go call sites) already calls
`dataset.BuildExample`/`dataset.Classify` for new pairs, this task is already
done — verify the double-write guard test exists and stop.

Rollback: revert the single commit. The wiring is additive to the "new pair"
branch only; reverting restores the prior behavior where labeled examples come
only from human-capture and the batch backfill op.