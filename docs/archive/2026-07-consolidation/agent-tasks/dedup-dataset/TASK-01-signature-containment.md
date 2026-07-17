&lt;!-- file: docs/agent-tasks/dedup-dataset/TASK-01-signature-containment.md --&gt;
&lt;!-- version: 1.0.0 --&gt;
&lt;!-- guid: b472c0c1-f21a-438f-8fc0-b8ee960b64e9 --&gt;
&lt;!-- last-edited: 2026-07-01 --&gt;

# TASK-01 — Offset/subsequence containment in signatureRelation (C5-sig)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dd-signature-containment" -b agent/dd-signature-containment origin/main
cd "$REPO/.worktrees/dd-signature-containment"
git rebase origin/main
```
Do all work inside that worktree. Never edit `main` or the primary checkout.

## Goal

`signatureRelation` in `internal/dedup/dataset/builder.go` currently only
distinguishes `match` / `disjoint` / `unknown` for a pair of books' whole-book
audio signatures. It cannot tell when one book's signature is a contiguous
sub-sequence of the other's (e.g. book B is just the first half of book A, or a
single-chapter excerpt of a larger recording). Extend it to detect this offset/
subsequence containment and return `a_contains_b` / `b_contains_a` in addition
to the existing three values.

## Background (verify before editing — line numbers drift)

- `internal/dedup/dataset/builder.go`, function `signatureRelation` (around
  lines 188-209 as of this writing). The doc comment explicitly says:
  `"Offset/subsequence containment (a_contains_b / b_contains_a) is deferred to
  a later spec milestone."` — that milestone is this task.
- The function currently calls `fingerprint.BookSignatureSimilarity(*a.BookSigV1, *b.BookSigV1)`
  and thresholds on `sigMatchThreshold` (find its value with grep — do not
  hardcode from memory).
- Look at the `fingerprint` package for any existing containment/subsequence
  helper before writing your own — check `internal/fingerprint/` for functions
  with names like `Contains`, `Subsequence`, `Offset`, or `Align`. If nothing
  suitable exists, implement the containment check directly on the two
  base64-decoded signature byte/hash sequences (however `BookSignatureSimilarity`
  decodes them — read that function first).

Run these to confirm the current state before editing:
```bash
grep -n "func signatureRelation" internal/dedup/dataset/builder.go
sed -n '185,215p' internal/dedup/dataset/builder.go
grep -n "sigMatchThreshold" internal/dedup/dataset/builder.go
grep -rn "func BookSignatureSimilarity" internal/fingerprint/
grep -rn "func.*Contains\|func.*Subsequence\|func.*Offset" internal/fingerprint/
```

## Step-by-step

1. Read `fingerprint.BookSignatureSimilarity` fully to understand the signature
   representation (e.g. a sequence of chroma/hash tokens) and how similarity is
   computed today.
2. Decide on a concrete containment test: the shorter signature's token
   sequence appears as a contiguous (allowing a small tolerance for minor
   edit noise, if the existing similarity function already tolerates noise —
   otherwise require an exact contiguous match) sub-sequence within the longer
   one, at some offset. If a sliding-window comparison is expensive for very
   long signatures, bound the work (e.g. skip a sliding-window check when
   token counts differ from typical audiobook signature lengths by an order
   of magnitude — comment why).
3. Add a small helper, e.g. `signatureContainment(shortTok, longTok []T) (offset int, ok bool)`,
   next to `signatureRelation` in `builder.go`. Keep it unexported and
   colocated — do not create a new file unless the existing file is already
   very large (check line count first with `wc -l`).
4. In `signatureRelation`, after the existing `match` check returns false
   (i.e. `sim < sigMatchThreshold`), before falling through to `disjoint`, call
   the new containment helper. If the shorter signature is contained in the
   longer one, return `a_contains_b` when `a`'s signature is the longer one
   containing `b`'s, or `b_contains_a` when it's the reverse. Otherwise fall
   through to the existing `disjoint` return.
5. Update the function's doc comment to list all five return values
   (`match`, `a_contains_b`, `b_contains_a`, `disjoint`, `unknown`) and remove
   the "deferred to a later spec milestone" sentence.
6. Bump the file header `version` (increment patch) and `last-edited` on every
   file you touch.

## How to test

Add a table-driven test in `internal/dedup/dataset/builder_test.go` (create it
if it does not exist — check first with `ls internal/dedup/dataset/*_test.go`)
covering: (a) identical signatures → `match`; (b) `b`'s signature is a
contiguous prefix/middle/suffix slice of `a`'s → `a_contains_b`; (c) the
reverse → `b_contains_a`; (d) unrelated signatures → `disjoint`; (e) nil/empty
`BookSigV1` → `unknown`.

```bash
go build ./...
go test ./internal/dedup/... ./internal/database/... -count=1
go vet ./internal/dedup/...
```

## Acceptance criteria

- [ ] `signatureRelation` returns `a_contains_b` / `b_contains_a` for contiguous
      sub-sequence containment cases, in addition to the existing `match` /
      `disjoint` / `unknown`.
- [ ] Doc comment on `signatureRelation` updated to list all five values and no
      longer says the containment case is deferred.
- [ ] New table-driven test covers all five outcomes and passes.
- [ ] `go test ./internal/dedup/... ./internal/database/... -count=1` passes;
      `go vet ./internal/dedup/...` clean.
- [ ] File headers bumped on every changed file.

## Commit message
```
feat(dedup): detect offset/subsequence containment in signatureRelation (C5-sig)

signatureRelation previously collapsed any non-matching signature pair to
disjoint. Add a containment check so one book's signature being a contiguous
sub-sequence of the other's (e.g. an excerpt or partial re-record) is now
reported as a_contains_b / b_contains_a instead of being lost as disjoint.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/dd-signature-containment
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Idempotency: if `signatureRelation` already returns `a_contains_b` /
`b_contains_a` anywhere and the doc comment no longer mentions "deferred to a
later spec milestone", this task is already done — verify the test file covers
all five cases and stop.

Rollback: revert the single commit; the change is additive (existing `match`/
`disjoint`/`unknown` return paths are unchanged, only a new branch is added
before the `disjoint` fallthrough).