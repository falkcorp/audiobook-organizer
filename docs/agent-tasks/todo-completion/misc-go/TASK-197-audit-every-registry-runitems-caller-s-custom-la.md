<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-197-audit-every-registry-runitems-caller-s-custom-la.md -->
<!-- version: 1.0.0 -->
<!-- guid: d562af56-5f5c-4fff-b622-3a2b8b2291e4 -->
<!-- last-edited: 2026-08-21 -->

# TASK-197 — Audit every registry.RunItems caller's custom Label closure for the post-fn re-render timing change (TODO.md L697)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · misc-go subagent · **Why:** breadth (31 files) with a narrow, mechanical check per file (does the Label closure read a counter/tally that fn itself mutates, and if so is post-fn rendering now correct or does it double-count) -- judgment-light but volume-heavy, not deep architecture work · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 697 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`registry.RunItems` label re-render (fixed 2026-08" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-197-audit-every-registry-runitems-caller-s-custom-la" -b agent/misc-go-197-audit-every-registry-runitems-caller-s-custom-la origin/main
cd "$REPO/.worktrees/misc-go-197-audit-every-registry-runitems-caller-s-custom-la"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For each of the 31 files listed in exact_files, read the Label: func(i, total int) string closure passed to registry.RunItems and determine whether it reads any variable that the corresponding per-item work function (fn, the second argument to RunItems) mutates. Classify each into one of three buckets and record the result: (A) Label reads only the loop index/item/total -- unaffected by the timing change, no action; (B) Label reads a running tally that fn mutates (e.g. a counter of matched/skipped/failed items) -- the post-fn re-render is now MORE correct (shows the tally including this item), confirm no double-counting bug was introduced (e.g. the tally must be incremented exactly once per item, not once in the label closure itself); (C) Label was written assuming the OLD pre-fn timing in a way the new post-fn timing breaks (e.g. it intentionally described the item ABOUT to be processed, and now shows post-completion state that reads as wrong to an operator watching the op's live progress feed) -- these need either a Label rewrite or an explicit code comment documenting the accepted behavior change. Produce a short per-file note (as a code comment where a fix is needed, otherwise no change) and fix every bucket-C finding.

## Background (verify before editing)

- internal/operations/registry/run_items.go:214-217 supplies a default Label when opt.Label is nil (`item %d/%d`), which is unaffected by this whole question since it reads no external mutable state.
- internal/operations/registry/run_items.go:225 declares `var completed atomic.Int64` and runOne (L226-253) is the shared driver every RunItems caller goes through regardless of its own Label closure -- SetCurrentItem gets the pre-work label (L233), UpdateProgress gets the post-work label (L245).
- The comment at run_items.go:240-242 cites a concrete prior symptom this fix corrected: chapters-backfill's Label showed 'persist=0' for ten of twelve lines on a run that actually persisted all twelve, because all N concurrent workers snapshotted their label string at dispatch time before any of them had incremented the tally fn was about to mutate.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "Re-render the label AFTER fn" internal/operations/registry/run_items.go   # 1 hit, comment starting at L236 — the label is now rendered after fn runs, not before
  git log --oneline -- internal/operations/registry/run_items.go | grep 9d2307a6   # 1 hit: 9d2307a6 fix(operations): report progress labels after the work, not before — the fix landed as a dedicated commit
  grep -rl 'Label:\s*func' internal/plugins | wc -l   # 31 — 31 distinct plugin files pass a custom Label closure to RunItems and are therefore in scope for the audit
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. For each file in exact_files, grep the file for `Label:\s*func` to locate the closure, then read the full closure body (usually 3-15 lines) and the fn closure passed as RunItems' third argument in the same call.
2. Identify every free variable the Label closure reads (typically a counter declared just above the RunItems call, e.g. `var matched, skipped int` or an atomic counter).
3. For each such variable, find every place fn increments/writes it. If fn increments it and the Label closure only reads it (never writes), this is bucket B -- correct today, no fix needed, but leave the finding out of any follow-up note since it is not a defect.
4. If the Label closure itself increments the counter (rather than fn), or if the counter represents 'items processed so far, not including this one' as an intentional design (i.e. the label is meant to describe what is about to start, not what just finished), this is bucket C: rewrite the Label closure to compute the count it wants directly from `i`/`total`/an atomic snapshot taken at the top of Label itself, rather than relying on ambient mutable state whose write timing relative to Label's call is now different.
5. For any bucket-C fix, add a one-line comment above the Label closure noting 'label re-rendered AFTER the item's fn runs (registry.RunItems, see run_items.go) -- computed from X, not ambient state' so a future reader does not reintroduce the same assumption.
6. After auditing all 31 files, if zero bucket-C findings exist, still leave a short comment in this PR's description (or a docs/ note, not required in exact_files) stating the audit was performed and found no violations -- do not silently close the TODO with no evidence.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_197.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A Label closure that reads a counter mutated by a DIFFERENT concurrent goroutine outside fn entirely (not one of RunItems' own workers) -- flag but do not attempt to fix under this item; note it as a separate finding, since that is a pre-existing concurrency question unrelated to the pre/post-fn timing change.
- Sequential (Concurrency=1) ops are lower-risk than parallel ones: in sequential mode the old pre-fn and new post-fn timing only differ by exactly one item's worth of state, and a bucket-C misread is easier to eyeball; in parallel mode multiple workers can each be mid-fn, so a shared tally read from Label is inherently racy unless the tally itself is atomic -- treat any non-atomic shared counter read from within a parallel op's Label closure as bucket C regardless of what it displays, and note it as a second, independent latent data race worth flagging even if the displayed number happens to look plausible.

## Tests

- For every file where a bucket-C fix is made, locate or write a maintenance-op progress test asserting the label text at a known item index matches the expected post-fn value -- follow the existing pattern in whichever _test.go file already covers that op's RunItems call (e.g. grep for existing progress/label assertions in the op's own _test.go before adding a new one).
- Run the full plugins test suite once all fixes land: go test ./internal/plugins/... -count=1

Anti-over-suppression test: `N/A -- this is a correctness audit, not a filter/guard/skip addition` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/acoustid/... ./internal/plugins/dedup/... ./internal/plugins/maintenance/... ./internal/plugins/metafetch/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -c 'Label:\s*func' across the 31 files still returns the same 38 call sites (no closures accidentally deleted)
- [ ] go build ./... && go vet ./... exit 0
- [ ] go test ./internal/plugins/... -count=1 passes
- [ ] Anti-over-suppression test: `N/A -- this is a correctness audit, not a filter/guard/skip addition` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/acoustid/... ./internal/plugins/dedup/... ./internal/plugins/maintenance/... ./internal/plugins/metafetch/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_197.md`.

## Commit message

```
refactor(misc-go): Audit every registry.RunItems caller's custom Label closure  (TODO L697)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Large-blast-radius audit (31 files) that is mechanical per-file but not safely choppable into independent parallel worktrees, since a false negative in a shared operations-registry concern benefits from one reader keeping the full picture in their head across files. Recommend running as a single sonnet-tier pass rather than splitting into 31 sub-tasks. review_critical=false because progress-label text is UI/observability only, not a data-mutation path -- but note the edge-case above (bucket-C-adjacent data races on non-atomic shared counters) could surface a real concurrency bug worth a follow-up TODO if found.
