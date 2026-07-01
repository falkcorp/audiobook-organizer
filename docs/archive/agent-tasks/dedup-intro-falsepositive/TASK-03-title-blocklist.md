<!-- file: docs/agent-tasks/dedup-intro-falsepositive/TASK-03-title-blocklist.md -->
<!-- version: 1.0.0 -->
<!-- guid: d7e9f1a3-5071-4d9e-9f30-6b8c0d2e4f60 -->
<!-- last-edited: 2026-06-28 -->

# TASK-03 — Title blocklist for publisher boilerplate

**Priority:** P3 · **Effort:** S · **Recommended subagent:** go-backend subagent ·
**Depends on:** TASK-01 (use its recurring-title list)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/di-title-blocklist" -b agent/di-title-blocklist origin/main
cd "$REPO/.worktrees/di-title-blocklist"
git rebase origin/main
```

## Goal

Some short clips are stored as their own "books" with boilerplate titles ("This
is Audible", "Audible hopes you have enjoyed this program", brand stings). These
should never seed a dedup match. Add a small, well-commented blocklist of
boilerplate title patterns and exclude matching books from the exact dedup layer.

## Background (verify before editing)

- Use the recurring-title list from TASK-01's `FINDINGS.md` as the seed.
- Find where a book/file enters the exact dedup layer (TASK-01) and where the
  title is available there.
- A normalized compare helper likely exists (`util.NormalizeTitle` in
  `internal/util/normalize.go`); reuse it for case/whitespace-insensitive matching.

## Step-by-step

1. Add a blocklist (slice of lowercased substrings or a small regexp), e.g.:
   ```go
   // boilerplateTitlePatterns: publisher intro/outro "titles" that are not real
   // books and must not seed dedup matches. Seeded from FINDINGS.md.
   var boilerplateTitlePatterns = []string{
       "this is audible",
       "audible hopes you",
       "an audible original",
       // ... add the rest from FINDINGS.md
   }
   ```
2. Add a helper `isBoilerplateTitle(title string) bool` using normalized
   substring matching.
3. In the exact dedup layer, skip a book/file whose title is boilerplate before
   emitting a candidate.
4. Keep the list small and commented; note in a code comment that it's seeded
   from `FINDINGS.md` and can be extended.
5. Bump file headers.

## How to test

`internal/dedup/*_test.go`: a book titled "This is Audible" does not pair with
anything; a normal book is unaffected; matching is case/whitespace-insensitive.

```bash
go build ./...
go test ./internal/dedup/ -count=1
go vet ./internal/dedup/
```

## Acceptance criteria

- [ ] Boilerplate-titled books are excluded from exact-layer matching.
- [ ] Matching is normalized (case/whitespace-insensitive).
- [ ] Normal titles unaffected; tests cover boilerplate-skip + normal-keep.
- [ ] `go test ./internal/dedup/` green; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
fix(dedup): blocklist publisher boilerplate titles from exact matching

Books whose title is publisher intro/outro boilerplate ("This is Audible", …)
are not real books and must not seed dedup pairs. Adds a small normalized
title blocklist (seeded from FINDINGS.md) applied in the exact layer.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/di-title-blocklist
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If a boilerplate-title blocklist already gates the exact layer, this is done.
Rollback = revert the commit.
