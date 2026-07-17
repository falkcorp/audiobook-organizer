<!-- file: docs/agent-tasks/dedup-hardening/TASK-01-exact-emitter-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0c572be2-2b62-416d-998d-74b294221785 -->
<!-- last-edited: 2026-07-01 -->

# TASK-01 — Boilerplate-title + min-duration guard at `upsertExactCandidate` chokepoint (dedup-residual)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dh-exact-emitter-guard" -b agent/dh-exact-emitter-guard origin/main
cd "$REPO/.worktrees/dh-exact-emitter-guard"
git rebase origin/main
```

## Goal

Close the residual DEDUP-INTRO-1 false-positive leak: add a boilerplate-title
guard and a minimum-duration guard **inside `upsertExactCandidate`** so
intro/outro-title and short-clip collisions cannot be persisted as exact dedup
candidates through *any* emitter, present or future. Reuse the existing
`isBoilerplateTitle` helper and the existing `minFingerprintMatchSeconds` (60s)
threshold — do not invent new constants or duplicate logic.

## Background (verify before editing)

- `upsertExactCandidate` in `internal/dedup/engine.go` is the single chokepoint
  all exact-layer emitters route through. As of this writing it applies ONLY
  two guards: `isNonPrimaryVersion` and `identifiersConflict`. It does **not**
  apply a boilerplate-title check or a minimum-duration check, even though both
  concepts already exist elsewhere in the file:
  - `isBoilerplateTitle(title string) bool` — defined near the top of
    `internal/dedup/engine.go` (title normalization + a blocklist of publisher
    intro/outro phrases like "this is audible", "audible studios presents").
  - `minFingerprintMatchSeconds = 60` — a package-level const documenting that
    files under 60s are publisher intro/outro clips, not book content.
  - Both are currently applied only in the fingerprint-seeding callers deep in
    the file (search for `isBoilerplateTitle(` and `knownShortFingerprintFile`
    for those call sites) — NOT in the shared chokepoint.
  - All exact emitters (`checkExactFileHash`, `checkExactISBN`,
    `checkExactISBNScan`, the metadata-hash checker, `checkExactTitle`, and the
    duration+title-distance checker) call `de.upsertExactCandidate(...)`. A
    guard added inside `upsertExactCandidate` therefore protects every current
    and future emitter with one change.
  - `database.Book.Duration` is `*int` (seconds). Use it for the minimum-duration
    check (treat `nil` or `<= 0` as "unknown" — do NOT skip on unknown duration,
    only skip when duration is positively known and short, matching the existing
    conservative pattern used by `hasPlausibleAudio`).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func (de \*Engine) upsertExactCandidate\|func isBoilerplateTitle\|const minFingerprintMatchSeconds\|func isNonPrimaryVersion\|func identifiersConflict" internal/dedup/engine.go
  ```
  Confirm `upsertExactCandidate`'s current body (should show the
  `isNonPrimaryVersion` check followed by the `identifiersConflict` check,
  before the `de.embedStore.UpsertCandidate(...)` call):
  ```bash
  grep -n "upsertExactCandidate" internal/dedup/engine.go
  ```

## Step-by-step

1. Open `internal/dedup/engine.go` and locate `upsertExactCandidate` (re-verify
   with the grep above — do not assume the line number from this brief).
2. Inside the function, after the existing `isNonPrimaryVersion` and
   `identifiersConflict` checks (before the `de.embedStore.UpsertCandidate`
   call), add two new guards:
   - **Boilerplate-title guard:** if `isBoilerplateTitle(a.Title)` or
     `isBoilerplateTitle(b.Title)` is true, log at `slog.Debug` level
     (mirroring the existing identifier-gate debug log's shape: fields
     `book_a`, `book_b`, `layer`) and `return nil` without upserting.
   - **Minimum-duration guard:** if either book has a known, positive
     `Duration` strictly less than `minFingerprintMatchSeconds` (60), log at
     `slog.Debug` level and `return nil` without upserting. Do NOT skip when
     `Duration` is `nil` or `<= 0` — those are "unknown", not "short", and must
     still be allowed through (matches the existing `hasPlausibleAudio`
     convention of treating unknown as non-disqualifying).
3. Keep both new guards purely additive — do not touch the existing
   `isNonPrimaryVersion` or `identifiersConflict` checks, and do not change the
   function signature or return type.
4. Add a new test file (or extend an existing `internal/dedup/*_test.go`) that:
   - Constructs two `database.Book` values where one has a boilerplate title
     (e.g. `"This is Audible. Audible hopes you have enjoyed this program."`)
     and calls `upsertExactCandidate` directly (or drives it via a real emitter
     if that's simpler in the existing test harness) — assert **no** candidate
     is persisted.
   - Constructs two books where one has `Duration` pointing to `30` (seconds,
     under the 60s threshold) — assert **no** candidate is persisted.
   - Constructs two books with normal titles and `Duration` of e.g. `3600`
     (1 hour) each — assert a candidate **is still** persisted (proves the new
     guards don't over-suppress real duplicates).
5. Bump the file header on every file you touch (version bump + `last-edited`
   date) per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/dedup/... -count=1
go vet ./internal/dedup/...
```

## Acceptance criteria

- [ ] `upsertExactCandidate` rejects any pair where either book has a
      boilerplate title (per `isBoilerplateTitle`), regardless of which emitter
      called it.
- [ ] `upsertExactCandidate` rejects any pair where either book has a known,
      positive `Duration` under `minFingerprintMatchSeconds` (60s).
- [ ] Books with unknown/zero duration are NOT rejected by the new duration
      guard (no over-suppression).
- [ ] A genuine duplicate pair (normal titles, both durations well above 60s)
      still produces a candidate.
- [ ] New/updated tests cover all three cases above; `go test ./internal/dedup/...`
      is green; `go vet` is clean.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(dedup): guard boilerplate-title and short-duration pairs in upsertExactCandidate (dedup-residual)

upsertExactCandidate only applied the non-primary-version and identifier-conflict
gates; boilerplate-title and minimum-duration checks existed only in the
fingerprint-seeding callers, leaving every other exact emitter exposed to
intro/outro-title and short-clip false positives. Move both guards into the
shared chokepoint so all six exact emitters are protected in one place.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dh-exact-emitter-guard
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `upsertExactCandidate` already calls `isBoilerplateTitle` and checks
`minFingerprintMatchSeconds` directly (not just in the fingerprint-seeding
callers), this task is done — verify with
`grep -n "isBoilerplateTitle\|minFingerprintMatchSeconds" internal/dedup/engine.go`
and confirm a call appears inside `upsertExactCandidate`'s body. Rollback =
revert the commit; the two pre-existing guards (`isNonPrimaryVersion`,
`identifiersConflict`) are untouched by this change and remain in effect.