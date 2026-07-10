<!-- file: docs/agent-tasks/dedup-label-quality/TASK-04-suspicious-label-queue.md -->
<!-- version: 1.0.0 -->
<!-- guid: eb2b02b0-6c46-4b1e-af81-f3646be80d66 -->
<!-- last-edited: 2026-07-10 -->

# TASK-04 — Suspicious-label review queue with one-click human override (INIT-1 T4)

**Gate:** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval. (This task's override writes are individual human-initiated UI actions — they are the intended human-labeling path, not a bulk prod mutation.)
**File-ownership:** shares `internal/server/handlers/dedup/label_review.go` with TASK-03 — this task runs in the wave AFTER TASK-03 merges; verify before starting. Do NOT touch `internal/dedup/engine.go` or `internal/database/embedding_store.go` (INIT-2-owned).

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · full-stack (Go handler + React) subagent · **Why:** a read-only filter endpoint plus a UI tab reusing an existing mutation route; two languages, no new architecture · **Depends on:** TASK-01 (BookFeatures.ASIN exists), TASK-03 (label_review.go settled)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-label-quality-suspicious-label-queue" -b agent/dedup-label-quality-suspicious-label-queue origin/main
cd "$REPO/.worktrees/dedup-label-quality-suspicious-label-queue"
git rebase origin/main
# Preconditions — ALL must hit, else STOP (TASK-01 and TASK-03 not merged yet):
grep -n "asin,omitempty" internal/database/dedup_label.go
grep -n "func SharesIdentity" internal/dedup/dataset/rules.go
grep -n "DedupeByPair" internal/server/handlers/dedup/label_review.go
```

## Goal

Grow human label volume cheaply: surface rule-sourced `not_dup` labels that carry duplicate-shaped evidence ("suspicious") in the existing Gold Labels UI, with a one-click override that REUSES the existing `POST /dedup/labels/:id/override` route — `OverrideDedupLabel` already stamps `label_source=human`, and the rebuild op preserves human rows forever, so every click permanently upgrades the gold set. Add one read-only endpoint (`GET /dedup/labels/suspicious`), one route line, and a "Suspicious" tab on `web/src/pages/DedupLabels.tsx`. Do NOT build a new override/mutation path.

## Background (verify before editing)

- Existing pieces to REUSE: `OverrideDedupLabel` handler in `internal/server/handlers/dedup/label_review.go` (stamps `labelSourceHuman`); route wired in `internal/server/wire_dedup_routes.go` as `POST /dedup/labels/:id/override`; list handler `ListDedupLabels` (same file) shows the response envelope + pagination shape to mirror; `EmbeddingStore.ListLabeledExamples` (in `internal/database/dedup_label.go`) is the read primitive. Human rows survive re-mining via the `case "human"` passthrough in `internal/plugins/dedup/rebuild_gold_labels.go`.
- Why these pairs are suspicious (2026-07-08 findings): 100% of `not_dup` labels are rule-sourced; the verified mislabels share ASIN or an identical path and/or show the ms/sec ratio signature (≈0.001) or sit at cosine ~1.0.
- `LabeledExample` fields available for the predicate: `Label`, `LabelSource`, `Band`, `Similarity *float64`, `DurationRatio`, `A`/`B BookFeatures` (with `ASIN`, `PrimaryPath`, `Title` — ASIN added by TASK-01). Note: historical rows have `ASIN == ""` until TASK-07's re-mine re-derives them — empty means unknown and simply doesn't fire that arm; the other arms still work on historical rows.
- The label set is ~7k rows — an in-handler Go filter over `ListLabeledExamples` output is fine; do NOT add a store-level index or touch `dedup_label.go`.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func (h \*Handler) OverrideDedupLabel' internal/server/handlers/dedup/label_review.go
  grep -n 'func (h \*Handler) ExportLabeledExamples' internal/server/handlers/dedup/label_review.go && grep -n 'func (s \*EmbeddingStore) ListLabeledExamples' internal/database/dedup_label.go
  grep -n "labels" internal/server/wire_dedup_routes.go                     # existing label routes incl. :id/override, >=4 hits
  grep -n "func (h \*Handler) ListDedupLabels" internal/server/handlers/dedup/label_review.go   # envelope to mirror, 1 hit
  grep -rn "dedup/labels" web/src/pages/DedupLabels.tsx | head -5           # existing API calls to mirror, >=1 hit
  ```
  If any grep returns 0 hits, STOP and report — do not guess.

## Step-by-step

1. `internal/server/handlers/dedup/label_review.go` — add `func (h *Handler) ListSuspiciousDedupLabels(c *gin.Context)`: load `not_dup` rows via `ListLabeledExamples(database.LabeledExampleFilter{Label: "not_dup", Limit: 0})`, then filter with a private predicate (name it exactly `isSuspiciousLabel`):
   ```go
   // isSuspiciousLabel: rule-sourced not_dup with duplicate-shaped evidence.
   func isSuspiciousLabel(ex database.LabeledExample) bool
   ```
   Fires when `ex.LabelSource == "rule"` AND any of: (a) `dataset.SharesIdentity(ex)` — REUSE the exported helper TASK-01 added in `internal/dedup/dataset/rules.go` (same ASIN / same version-group / identical primary path); do NOT re-implement identity comparisons in the handler — one implementation, two consumers (import `internal/dedup/dataset`; it imports only `internal/database`, no cycle); (b) `ex.Band == "CERTAIN" || ex.Band == "HIGH"`; (c) `ex.Similarity != nil && *ex.Similarity >= 0.95`; (d) `ex.A.Title != "" && ex.A.Title == ex.B.Title && ex.DurationRatio > 0 && ex.DurationRatio < 0.01` (the ms/sec signature). Empty/unknown fields never fire an arm (non-disqualifying — the row just isn't suspicious on that evidence). Mark arm (a) with a comment as **TRANSITIONAL**: after TASK-07 re-mines with TASK-01's guard, identity-sharing pairs are emitted as `unsure` (not rule `not_dup`), so (a) can only surface the historical pre-re-mine backlog and then goes quiet — the queue's durable value is arms (b)/(c)/(d). Include a per-row `suspicion_reasons []string` in the response so the UI can show WHY. Mirror `ListDedupLabels`'s response envelope and pagination params.
2. `internal/server/wire_dedup_routes.go` — add, next to the existing label routes: `protected.GET("/dedup/labels/suspicious", s.perm(auth.PermLibraryView), dedupH.ListSuspiciousDedupLabels)`. Use the same `perm` middleware pattern as the sibling GET routes (view perm — it is read-only).
3. `web/src/pages/DedupLabels.tsx` — add a "Suspicious" tab (mirror the page's existing tab/filter mechanics): fetch `/dedup/labels/suspicious`, render the pair (titles, authors, paths, durations, `suspicion_reasons`), and per row two buttons — "Mark true_dup" and "Confirm not_dup" — each POSTing the EXISTING `/dedup/labels/{id}/override` endpoint with the corresponding label (copy the request shape from the page's existing override usage if present; otherwise from `OverrideDedupLabel`'s request struct). On success remove the row from the list.
4. Purely additive: do not modify existing tabs, `ListDedupLabels`, `ExportLabeledExamples`, `OverrideDedupLabel`, or any route besides the one new GET line.
5. Tests:
   - Go — extend the handler test file next to `label_review.go`'s existing tests: `TestSuspiciousPredicateIdentity` (the `dataset.SharesIdentity` arm: ASIN, version-group, and path variants — the helper's own unit tests live in TASK-01; here assert the ARM fires through it), `...Band`, `...Similarity`, `...MsSecSignature`, and the anti-over-suppression case `TestSuspiciousPredicateCleanRuleLabelNotFlagged` (a rule `not_dup` with disjoint ASINs/paths, LOW band, similarity 0.5, sane ratio 0.3 → NOT suspicious). Also `TestSuspiciousPredicateHumanLabelNeverFlagged` (human-sourced rows are excluded regardless of evidence).
   - Frontend — extend `web/src/pages/__tests__/DedupLabels.test.tsx`: tab renders rows from a mocked suspicious response; clicking "Mark true_dup" POSTs the override URL.
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test ./internal/server/handlers/dedup/... -race
go test ./... -short
make test-all    # includes the frontend suite for DedupLabels.test.tsx
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "ListSuspiciousDedupLabels" internal/server/wire_dedup_routes.go` hits (route wired, view-perm GET)
- [ ] `grep -n "func isSuspiciousLabel" internal/server/handlers/dedup/label_review.go` hits
- [ ] `grep -n "dataset.SharesIdentity" internal/server/handlers/dedup/label_review.go` hits (identity arm reuses TASK-01's helper — no second identity implementation in the handler)
- [ ] `go test ./internal/server/handlers/dedup/ -run TestSuspicious -v` — all arms pass, including `TestSuspiciousPredicateCleanRuleLabelNotFlagged` (anti-over-suppression: clean rule labels are NOT flagged) and `...HumanLabelNeverFlagged`
- [ ] empty/unknown fields don't fire arms (asserted inside the predicate tests: a row with all-empty identity + no band/similarity is not suspicious)
- [ ] `grep -n "Suspicious" web/src/pages/DedupLabels.tsx` hits; frontend test green
- [ ] no new mutation endpoint: `grep -c "POST" internal/server/wire_dedup_routes.go` is unchanged from before this task (verify via `git diff origin/main -- internal/server/wire_dedup_routes.go` showing only one added GET line)
- [ ] `go test ./... -short` green; `make ci` green
- [ ] File headers bumped on every changed file

## Commit message

```
feat(dedup): suspicious-label review queue with one-click human override (INIT-1 T4)

Surfaces rule-sourced not_dup labels carrying duplicate-shaped evidence
(shared ASIN/path, CERTAIN/HIGH band, cosine >= 0.95, ms/sec ratio signature)
in the Gold Labels UI. One-click override reuses POST /dedup/labels/:id/override,
which stamps label_source=human — rows the re-mine permanently preserves.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-label-quality-suspicious-label-queue
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "ListSuspiciousDedupLabels" internal/server/wire_dedup_routes.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the queue is read-only, and any overrides already clicked are legitimate human labels (individually re-overridable via the same route) — reverting the UI does not and must not undo them.
