<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-051-acoustic-confirm-signal-promote-near-dupe-title-.md -->
<!-- version: 1.0.0 -->
<!-- guid: e8f9900c-5ee9-41d9-9cb1-6751d5edfc5f -->
<!-- last-edited: 2026-08-21 -->

# TASK-051 — Acoustic-confirm signal: promote near-dupe title-leak pairs using WholeFileSimilarity (TODO.md L10750)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · dedup subagent · **Why:** modifies the auto-merge eligibility gate on a prod-data-mutating path (dedup merges); must not weaken the existing suppressor/plausible-audio/identifier-conflict guards, only add a new corroborating path alongside them · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10750 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Fingerprint-confirmed dedup + shattered-book rea" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-051-acoustic-confirm-signal-promote-near-dupe-title-" -b agent/dedup-051-acoustic-confirm-signal-promote-near-dupe-title- origin/main
cd "$REPO/.worktrees/dedup-051-acoustic-confirm-signal-promote-near-dupe-title-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new corroboration path to `autoResolveEligible` (internal/dedup/auto_resolve.go): when both sides of a CERTAIN-band candidate have an AcoustID fingerprint, compute `fingerprint.WholeFileSimilarity` between them and, if it is close enough (a new configured threshold, e.g. `config.AppConfig.Dedup.AcousticConfirmThreshold`), count it as satisfying the corroboration requirement (either as one more 'distinct primary signal kind' or as its own fallback branch mirroring the existing whole-book-signature-label branch), so a 'same file, one extra character' title-leak near-dupe pair that would otherwise fall 1 signal short of auto-merge eligibility gets confirmed — while a genuinely distinct pair (different WholeFileSimilarity) still falls back to today's scoring, unchanged.

## Background (verify before editing)

- Scope text: 'where both sides of a candidate pair are fingerprinted, use WholeFileSimilarity closeness as a *confirming* signal to auto-promote the "same file, one extra character" title-leak near-dupes to auto-merge; distinct pairs fall back to today's scoring.'
- internal/dedup/auto_resolve.go:213-272's existing structure: band must be CERTAIN, no active suppressors, PairEligibility must pass live, plausible-audio + no identifier conflict, THEN either ≥2 distinct primary signal kinds OR a whole-book-signature true_dup label. The new acoustic-confirm path is a third alternative at the same tier as the true_dup-label fallback, not a replacement for any existing guard.
- This must stay behind `config.AppConfig.Dedup.AutoResolveEnabled` for apply=true, per auto_resolve.go:122's existing kill-switch pattern — report-only (apply=false) already always works regardless, per the comment at L96-99.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n WholeFileSimilarity internal/dedup/engine.go internal/dedup/collectors_acoustid.go internal/fingerprint/wholefile.go   # 3 hits across the 3 files — WholeFileSimilarity already used as a scoring signal
  grep -n 'DistinctAutoResolvePrimaryKinds\|whole-book-signature true_dup' internal/dedup/auto_resolve.go   # ≥2 hits ~L250,262 — autoResolveEligible's corroboration gate and its existing fallback branch
  grep -n AutoResolveEnabled internal/dedup/auto_resolve.go   # ≥2 hits ~L96-122 — AutoResolveEnabled kill switch gates all apply=true auto-resolve calls
  ```

### Reuse — don't invent

- Use `WholeFileSimilarity` in `internal/fingerprint/wholefile.go` (verify: `grep -n 'func WholeFileSimilarity' internal/fingerprint/wholefile.go`) — do NOT write a parallel helper.
- Use `autoResolveEligible's existing fallback-branch pattern to copy` in `internal/dedup/auto_resolve.go` (verify: `grep -n 'GetLabeledExample' internal/dedup/auto_resolve.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add a new field to `DedupConfig` in internal/config/config.go (~L260-341, near other Dedup* fields) for the acoustic-confirm similarity threshold, e.g. `AcousticConfirmThreshold float64` with a sane conservative default (start high, e.g. 0.97+, to bias toward false-negative over false-positive on an auto-merge path).
2. In internal/dedup/auto_resolve.go's `autoResolveEligible`, after the existing `distinct := DistinctAutoResolvePrimaryKinds(...)` check (L250) fails to reach 2, before falling through to the whole-book-signature-label check (L259), add: if `bookA.AcoustIDFingerprint != "" && bookB.AcoustIDFingerprint != ""`, compute `sim, err := fingerprint.WholeFileSimilarity(bookA.AcoustIDFingerprint, bookB.AcoustIDFingerprint)` and if `err == nil && sim >= config.AppConfig.Dedup.AcousticConfirmThreshold`, return `true, fmt.Sprintf("acoustic confirm: WholeFileSimilarity=%.4f", sim)`.
3. Do NOT touch the existing suppressor/plausible-audio/identifier-conflict guards above this point — they must still gate the acoustic-confirm path exactly as they gate the other two paths (this is enforced by placement: the new check happens after those guards, inside the same function, so it inherits them for free).
4. Bump the file-header version on internal/dedup/auto_resolve.go and internal/config/config.go.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_051.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- One side has an AcoustIDFingerprint and the other doesn't — must fall through to the existing false/insufficient-corroboration path, never treat a missing fingerprint as a match (mirrors the project's standing 'absent evidence means cannot verify, never refuted' rule already documented elsewhere in TODO.md for the same signal).
- WholeFileSimilarity itself returns an error (e.g. malformed fingerprint bytes) — must be treated as 'no confirmation', not silently ignored into a panic or a false positive.

## Tests

- internal/dedup/auto_resolve_test.go (check for existing coverage first: `grep -n autoResolveEligible internal/dedup/auto_resolve_test.go`) — add a case: 2 books, CERTAIN band, only 1 distinct primary signal, but AcoustIDFingerprint set on both with WholeFileSimilarity above threshold → eligible=true with the new reason string.
- A second case: same setup but WholeFileSimilarity below threshold → eligible=false, unchanged reason ('insufficient corroboration...').
- A case with the acoustic-confirm signal present AND an active suppressor → eligible=false (suppressor still wins; proves the new path did not bypass existing guards).

Anti-over-suppression test: `test: 'a genuinely distinct pair (fingerprinted, similarity below threshold, only 1 distinct signal kind) is correctly NOT auto-resolved — the new acoustic-confirm path does not make the gate looser for non-matching pairs'` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/dedup/... -run TestAutoResolveEligible` (or the actual test function name found above) passes including the 3 new cases.
- [ ] `make ci` passes.
- [ ] Anti-over-suppression test: `test: 'a genuinely distinct pair (fingerprinted, similarity below threshold, only 1 distinct signal kind) is correctly NOT auto-resolved — the new acoustic-confirm path does not make the gate looser for non-matching pairs'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_051.md`.

## Commit message

```
feat(dedup): Acoustic-confirm signal: promote near-dupe title-leak pairs  (TODO L10750)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (``go test ./internal/dedup/... -run TestAutoResolveEligible` (or the actual test function name found above) passes including the 3 new cases.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: this directly changes what gets auto-merged in production dedup. Keep the threshold conservative and behind AutoResolveEnabled; do not change existing signal-kind or suppressor logic.
