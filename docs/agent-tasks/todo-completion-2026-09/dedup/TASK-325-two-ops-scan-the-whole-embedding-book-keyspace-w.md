<!-- file: docs/agent-tasks/todo-completion-2026-09/dedup/TASK-325-two-ops-scan-the-whole-embedding-book-keyspace-w.md -->
<!-- version: 1.7.0 -->
<!-- guid: d336ec1f-6a62-4705-adc5-f1acb4d73379 -->
<!-- last-edited: 2026-09-10 -->

# TASK-325 — Two ops scan the whole embedding/book keyspace with a plain sequential loop doing a per-item DB point-lookup, unlike every sibling op in the same package which uses registry.RunItems -- the exact single-threaded-hotspot shape CLAUDE.md's concurrency mandate exists to catch (DA-03)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `DA-03` (audit_dedup_activity.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · dedup subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `DA-03` (audit_dedup_activity.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-325" -b agent/dedup-325-two-ops-scan-the-whole-embedding-book-ke origin/main
cd "$REPO/.worktrees/dedup-325"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Convert both scan loops to registry.RunItems with Concurrency: runtime.NumCPU(), guarding the shared report counters/slices with a mutex (same shape as quarantine_chapter_artifacts.go); or, cheaper, mirror the author-side op's pattern and precompute a live-book-ID set from one bounded GetAllBooksCore paginated scan instead of one GetBookByID per embedding.

Why it matters: CLAUDE.md's concurrency mandate exists specifically because of the dedup.full-scan single-core 3h+ stall (2026-07-05); engine.go's FullScan was subsequently reworked into a sharded worker pool for exactly this reason (CONC-2/CONC-4 comments in engine.go). These two ops iterate the same order-of-magnitude collection (every book-embedding row / every book) doing one synchronous KV point-get per item, and on a library the size this codebase operates at (tens of thousands to 100K+ books/embeddings, per docs/AI-REFERENCE.md and project notes) that is minutes of single-core wall time for an op with a 30-minute timeout and no other progress signal beyond periodic percentage logs.

## Background (verify before editing)

- scanOrphanEmbeddings (cleanup_orphan_embeddings.go:181-220) is a plain `for i := range embeddings` loop over every emb:v:book:* row calling p.store.GetBookByID(e.EntityID) synchronously per item (line 200) -- a full library-scale point-lookup per embedding, no worker pool. The apply-path delete loop (lines 148-164) is likewise a plain sequential `for _, id := range report.OrphanIDs { p.embeddingStore.Delete(...) }`. reembed_embeddings.go's Phase-1 scan (lines 164-214) does the same thing in reverse: for every book returned by paginated GetAllBooksCore, it calls p.embeddingStore.Get("book", b.ID) synchronously per book (line 185). By contrast every other per-item op added to this same package already follows the mandated worker-pool pattern with Concurrency explicitly set: build_candidate_index.go:167-168, mine_gold_labels.go:160-161, quarantine_chapter_artifacts.go:184-185 and 230-231, breakdown_backfill.go:504-505, dataset_backfill.go:286-287, lsh_index_build.go:321-322, rescore_labeled_examples.go:331-332 (all `registry.RunItems(..., registry.RunItemsOptions{Concurrency: runtime.NumCPU(), ...})`). The sibling cleanup_orphan_author_embeddings.go avoids the per-item DB call entirely by precomputing a live-ID set from one GetAllAuthors() call, and its own doc comment (lines 194-201) explicitly contrasts itself with 'the book op's per-row GetBookByID lookups' -- i.e. the codebase's own comments already name this exact hotspot in the sibling file without fixing it.
- Anchor: `internal/plugins/dedup/cleanup_orphan_embeddings.go:184` (audit `DA-03`, confidence high, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/plugins/dedup/cleanup_orphan_embeddings.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '142,226p' internal/plugins/dedup/cleanup_orphan_embeddings.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/plugins/dedup/cleanup_orphan_embeddings.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_dedup_325.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/dedup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_dedup_325.md`.

## Commit message

```
fix(dedup): Two ops scan the whole embedding/book keyspace with a plain sequential (DA-03)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold).

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
