<!-- file: docs/agent-tasks/dedup-intro-falsepositive/TASK-04-booklevel-isbn-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: e8f0a2b4-6182-4e0f-9a41-7c9d1e3f5071 -->
<!-- last-edited: 2026-06-28 -->

# TASK-04 — Book-level ISBN/ASIN gate before file match

**Priority:** P2 · **Effort:** M · **Recommended subagent:** go-backend subagent ·
**Depends on:** TASK-01 (mismatch-rate evidence)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/di-isbn-gate" -b agent/di-isbn-gate origin/main
cd "$REPO/.worktrees/di-isbn-gate"
git rebase origin/main
```

## Goal

A shared file fingerprint should not merge two books that are provably different
editions/titles. Add a book-level gate: if the two books in a candidate pair have
**different, both-present** ISBN10/ISBN13/ASIN, drop the pair regardless of a
shared short-clip fingerprint. Identifiers are authoritative; a shared jingle is
not.

## Background (verify before editing)

- `Book` carries `ISBN10`, `ISBN13`, `ASIN` (see `internal/database/store.go`).
- There is likely an ISBN/ASIN secondary index and helpers
  (`derefStrISBN`, `writeISBNIndexRows`, `GetBookIDsByISBNASIN`) in
  `internal/database/pebble_store.go`. Find compare helpers:
  ```bash
  grep -rn "ISBN10\|ISBN13\|ASIN\|derefStrISBN\|GetBookIDsByISBN" internal/database/ internal/dedup/ | head
  ```
- Apply at the candidate-emit point identified in TASK-01.

## Step-by-step

1. Add a helper:
   ```go
   // identifiersConflict reports whether a and b have a definitive, both-present
   // identifier mismatch (different ISBN13, or different ISBN10, or different
   // ASIN). Missing identifiers are NOT a conflict (return false).
   func identifiersConflict(a, b *database.Book) bool { ... }
   ```
   Compare each of ISBN13/ISBN10/ASIN only when BOTH books have that field set
   (normalize: trim, strip hyphens, uppercase ASIN). Any one definitive mismatch
   → true.
2. In the exact dedup layer, after a pair is formed but before it's emitted, drop
   the pair when `identifiersConflict(a, b)` is true.
3. Be conservative: when identifiers are missing on either side, do NOT drop
   (let the other layers decide).
4. Log a debug counter of pairs dropped by the identifier gate.
5. Bump file headers.

## How to test

`internal/dedup/*_test.go`: two books with different ISBN13s sharing a fingerprint
do NOT pair; two books with the same ISBN13 still pair; two books where one lacks
an ISBN still pair (no over-gating).

```bash
go build ./...
go test ./internal/dedup/ -count=1
go vet ./internal/dedup/
```

## Acceptance criteria

- [ ] Pairs with a definitive both-present ISBN13/ISBN10/ASIN mismatch are dropped.
- [ ] Missing identifiers never cause a drop (conservative).
- [ ] Identifier compare is normalized (hyphens/case).
- [ ] Tests cover mismatch-drop / match-keep / missing-keep; `go test ./internal/dedup/` green; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
fix(dedup): drop candidate pairs with conflicting ISBN/ASIN identifiers

A shared short-clip fingerprint must not merge two books that have different,
both-present ISBN13/ISBN10/ASIN — identifiers are authoritative. Missing
identifiers never gate (conservative).

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/di-isbn-gate
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If an ISBN/ASIN conflict gate already exists on the exact layer, this is done.
Rollback = revert the commit.
