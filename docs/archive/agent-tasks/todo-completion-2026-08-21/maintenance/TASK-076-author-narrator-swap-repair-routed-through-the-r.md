<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-076-author-narrator-swap-repair-routed-through-the-r.md -->
<!-- version: 1.1.0 -->
<!-- guid: f39b31b0-6002-4b12-bf6c-51bb7b3df9c1 -->
<!-- last-edited: 2026-09-02 -->

# TASK-076 — Author-narrator swap repair, routed through the review queue (cross-table population, distinct from the existing per-book fix-author-narrator-swap job) (TODO.md L5281)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — author_narrator_swap_review.go ABSENT; 'swap-shaped\|AuthorNarratorSwapCandidate' -> 0 hits. All 5 anchors hit (fix_author_narrator_swap.go:70,86; iface_review.go:12; regroup_shattered_ai.go:270). Recommendation: keep.

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · maintenance subagent · **Why:** New cross-table detection heuristic plus review-queue integration on a prod-data path (author/narrator identity) — requires careful design of the DedupKey/Payload shape and of what a human reviewer sees to approve/reject, not mechanical. · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 5281 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Author↔narrator swap repair.** Measured lower bo" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-076-author-narrator-swap-repair-routed-through-the-r" -b agent/maintenance-076-author-narrator-swap-repair-routed-through-the-r origin/main
cd "$REPO/.worktrees/maintenance-076-author-narrator-swap-repair-routed-through-the-r"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a new read-only detection pass (e.g. a new maintenance op, modeled structurally on author_purge_empty.go's dry-run-by-default shape) that finds names present in BOTH the author and narrator tables where the narrator side has >=5 books and the author side has 1-2 books (mirroring the item's measured 67-name/96-link lower bound), and for each candidate UPSERTs a ReviewItem (Kind e.g. 'author_narrator_swap', DedupKey derived from the shared name, Payload carrying the author ID, narrator ID, both book counts, and the affected book IDs/titles) via database.Store's ReviewStore.UpsertReviewItem — never applying the swap directly. A human then approves/rejects through the existing review-queue UI/API, and a SEPARATE apply step (out of scope for this item; note it as a follow-on) reads approved items and performs the actual author<->narrator correction.

## Background (verify before editing)

- TODO item measures (2026-08-17): 1,052 names appear in both the author and narrator tables; 67 are swap-shaped (narrates >=5 books, 'authors' with only 1-2 books), accounting for ~96 book-author links; explicit named examples Ray Porter, Scott Brick, Nick Podehl, Andrea Parsneau exist as authors.
- This is a LOWER BOUND by the item's own admission — the rule only sees names present in BOTH tables, so a swap whose 'author' string never separately appears as a narrator elsewhere is invisible to it. Note this limitation in the new op's doc comment so it isn't mistaken for a complete detector.
- internal/database/iface_review.go's ReviewStore (PR-A1) is the 'universal review-queue surface... for a generic, producer-agnostic queue of items flagged for a human decision' — exactly the mechanism this item asks the repair to route through, and it is already used by at least one other maintenance producer (regroup_shattered_ai.go) as a model.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'UpdateBook(book.ID' internal/maintenance/jobs/fix_author_narrator_swap.go   # 1 hit, L86, inside an if !dryRun block with no UpsertReviewItem call anywhere in the file — the existing fix-author-narrator-swap job blind-applies with no review-queue call
  grep -n 'EqualFold(author.Name' internal/maintenance/jobs/fix_author_narrator_swap.go   # 1 hit, L70 — the existing job's detection rule is same-book field equality, not cross-table name overlap
  grep -rn 'swap-shaped\|AuthorNarratorSwapCandidate' internal/   # 0 hits outside docs/TODO — no code implements the cross-table swap-shaped detection this item describes
  grep -n 'type ReviewStore interface' internal/database/iface_review.go   # 1 hit, L12 — the generic review queue this item asks to route through exists and is producer-agnostic
  grep -n 'UpsertReviewItem(item)' internal/plugins/maintenance/regroup_shattered_ai.go   # 1 hit, L270 — an existing producer shows the UpsertReviewItem call pattern to model on
  ```

### Reuse — don't invent

- Use `ReviewStore.UpsertReviewItem + ReviewItem struct (Kind/DedupKey/FolderRef/Payload/Summary)` in `internal/database/review_store.go` (verify: `grep -n 'type ReviewItem struct' internal/database/review_store.go`) — do NOT write a parallel helper.
- Use `regroup_shattered_ai.go's UpsertReviewItem call site (model for a new producer)` in `internal/plugins/maintenance/regroup_shattered_ai.go` (verify: `grep -n 'res, uerr := store.UpsertReviewItem(item)' internal/plugins/maintenance/regroup_shattered_ai.go`) — do NOT write a parallel helper.
- Use `ListNarrators / GetAllAuthorBookCounts (existing count sources to build the cross-table scan on)` in `internal/database/pebble_store_authors.go` (verify: `grep -n 'func (p \*PebbleStore) ListNarrators\|func (p \*PebbleStore) GetAllAuthorBookCounts' internal/database/pebble_store_authors.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/plugins/maintenance/author_narrator_swap_review.go with a new op ID e.g. 'maintenance.detect-author-narrator-swaps', dry-run/report-only always (this op never applies anything — it only populates the review queue for a human to act on).
2. Detection: call store.ListNarrators() and (whatever the equivalent all-authors-with-book-counts call is, e.g. GetAllAuthorBookCounts plus a name lookup) to build two maps keyed by normalized name; for every name present in both, compute narrator book count (via GetNarratorsByBookIDs or a similar existing lookup — check for an author-book-count analog on the narrator side; if none exists, count via book_narrators: scan) and author book count (GetAllAuthorBookCounts, already used by author_purge_empty.go); flag a candidate when narrator book count >= 5 AND author book count is 1 or 2.
3. For each candidate, build a ReviewItem{Kind: "author_narrator_swap", DedupKey: hash of the shared normalized name (so a re-run upserts idempotently, matching every other reviewstore producer's contract), Summary: e.g. "'<name>' narrates N books but is credited as author on M", Payload: JSON with author ID, narrator ID, affected book IDs} and call store.UpsertReviewItem(item), following regroup_shattered_ai.go's call pattern.
4. Register the new op in internal/plugins/maintenance/plugin.go's RegisterOps list, following the existing purgeEmptyAuthorsDef() registration pattern.
5. Document in the op's header comment (mirroring author_purge_empty.go's style) that this is explicitly a LOWER BOUND and that the actual repair (moving the swapped credit) is a follow-on apply step reading APPROVED review items, out of scope for this op.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_076.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A name matching case-insensitively but with different punctuation/spacing between the author and narrator tables — use the same util.NormalizeAuthor normalization CreateAuthor/CreateNarrator already use for their name indexes, so the cross-table join is consistent with how the rest of the codebase treats 'the same name'.
- A name that is a swap candidate but the author-side row is ALSO the target of L5290's DeleteAuthor junction-cleanup bug or L5271's purge-empty-authors — do not let this op delete or merge anything; it only queues a review item, so those other ops' behavior is unaffected by (and should not be blocked by) this one.
- Re-running after a human REJECTS a review item — UpsertReviewItem's documented idempotency contract (per iface_review.go's comment) never resurfaces an already-decided non-pending item, so a rejected candidate should not reappear on the next scan; verify this is exercised by an existing ReviewStore test rather than re-deriving it here.

## Tests

- internal/plugins/maintenance/author_narrator_swap_review_test.go — TestDetectAuthorNarratorSwaps_FlagsCandidate: mock store with a narrator having 6 books under name X and an author named X with 1 book; assert exactly one UpsertReviewItem call with Kind 'author_narrator_swap' and the correct DedupKey/Payload contents.
- TestDetectAuthorNarratorSwaps_SkipsBelowThreshold: narrator with 4 books (below the >=5 threshold) under a name that also exists as an author with 1 book — assert NO UpsertReviewItem call (this is the anti-over-suppression / precision test — without it, a future tweak to the threshold could silently flood the review queue with weak candidates).
- TestDetectAuthorNarratorSwaps_SkipsAuthorWithManyBooks: narrator with 6 books under a name that also exists as an author with 8 books (a genuinely prolific person doing both roles, not a swap) — assert NO UpsertReviewItem call.
- TestDetectAuthorNarratorSwaps_Idempotent: run detection twice against the same data — assert the second run's UpsertReviewItem call uses the identical DedupKey as the first (proves re-running the scan does not duplicate review items).

Anti-over-suppression test: `TestDetectAuthorNarratorSwaps_SkipsBelowThreshold` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestDetectAuthorNarratorSwaps passes
- [ ] make ci passes
- [ ] grep -n 'maintenance.detect-author-narrator-swaps' internal/plugins/maintenance/plugin.go returns 1 hit (registered)
- [ ] Anti-over-suppression test: `TestDetectAuthorNarratorSwaps_SkipsBelowThreshold` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_076.md`.

## Commit message

```
fix(maintenance): Author-narrator swap repair, routed through the review queue (TODO L5281)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — `grep -n 'maintenance.detect-author-narrator-swaps' internal/plugins/maintenance/plugin.go returns 1 hit (registered)` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The existing internal/maintenance/jobs/fix_author_narrator_swap.go job is NOT what this item is asking to fix or extend — it solves a narrower, different problem (same-book field equality) and predates the review-queue infrastructure. Do not conflate the two in a PR description; flag both to the coordinator as related-but-distinct so a reviewer doesn't assume this item is already covered.
