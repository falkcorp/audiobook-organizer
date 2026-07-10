<!-- file: docs/agent-tasks/metadata-matching/TASK-08-fuzzy-token-set.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0a66283f-16aa-4f56-9287-756aecfd1300 -->
<!-- last-edited: 2026-07-10 -->

# TASK-08 — Token-set fuzzy scoring upgrade (INIT-3-T6) — OPTIONAL / P3

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero behavior change until an operator tunes them.
**File-ownership:** none (`internal/matcher/fuzzy.go` touched by no sibling task). This task is OPTIONAL — skipping it blocks nothing; confirm with the coordinator that it is wanted before spending effort.

**Priority:** P3 · **Effort:** M · **Recommended subagent:** Sonnet-class · algorithm subagent · **Why:** small file, but blending a new similarity signal into live matching needs fixture-locked regression care · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-matching-fuzzy-token-set" -b agent/metadata-matching-fuzzy-token-set origin/main
cd "$REPO/.worktrees/metadata-matching-fuzzy-token-set"
git rebase origin/main
```

## Goal

Add an order-insensitive token-set similarity to `internal/matcher/fuzzy.go` —
`func TokenSetRatio(a, b string) float64` (0..1; tokenize via the file's existing `normalize`,
compare token sets à la fuzzywuzzy token_set_ratio) — and blend it into `ScoreMatch` ONLY as an
additional signal that can raise (never lower) a score, with fixture-locked before/after evidence
that known-good matches don't regress. REUSE the existing `normalize` helper and
`LevenshteinDistance`; do NOT add a third-party dependency or a new similarity package.

## Background (verify before editing)

- `internal/matcher/fuzzy.go` is purely lexical today: `LevenshteinDistance` (~20, single-row DP),
  `ScoreMatch` (~54, exact/prefix/substring/word-start checks + two Levenshtein-based terms),
  `RankResults` (~125), `normalize` (~143). No token-reorder robustness: "The Hobbit — Tolkien"
  vs "Tolkien: The Hobbit" scores poorly.
- Blast radius — `ScoreMatch`/`RankResults` callers (verify, then read each call site's threshold
  usage before changing any returned scale): `internal/matcher/matcher.go`,
  `internal/scanner/scanner.go`, `internal/itunes/service/path_repair_resolver.go`.
- `ScoreMatch` returns an `int` score compared against caller thresholds (`minScore` in
  `RankResults`). The blend must keep the existing scale and NEVER DECREASE any pair's existing
  score — raise-only, so no caller threshold can newly reject a previously-accepted match.
- Edge semantics: empty/whitespace-only input → `TokenSetRatio` returns 0 (unknown, not a match);
  single-token strings degrade gracefully to plain comparison; the function must be symmetric
  (`TokenSetRatio(a,b) == TokenSetRatio(b,a)`).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func LevenshteinDistance\|func ScoreMatch' internal/matcher/fuzzy.go
  grep -n 'func RankResults\|func normalize' internal/matcher/fuzzy.go
  grep -rn "matcher\." internal/scanner/scanner.go internal/itunes/service/path_repair_resolver.go internal/matcher/matcher.go | head -20
  ```
  Zero hits on the fuzzy.go greps = STOP and report drift.

## Step-by-step

1. **Fixture-capture first:** in `internal/matcher/fuzzy_test.go`, add a table
   `TestScoreMatchGolden` capturing CURRENT `ScoreMatch` outputs for ~20 representative pairs
   (exact, prefix, substring, reordered-tokens, author-appended, typo'd, unrelated). Commit
   separately.
2. Implement `TokenSetRatio(a, b string) float64` using `normalize` + field-splitting: intersection
   and difference token sets, Levenshtein-similarity of the sorted joined sets (standard
   token_set_ratio construction). Symmetric; empty → 0.
3. Blend into `ScoreMatch` raise-only: compute the existing score, then
   `if tokenScore := int(TokenSetRatio(query, target) * <existing max-scale>); tokenScore > score { score = tokenScore }`
   (read the existing code to pin the actual scale before choosing the multiplier — mirror how the
   two existing Levenshtein terms map to the int scale).
4. Update `TestScoreMatchGolden`: reordered-token rows may rise (enumerate each with a comment);
   NO row may fall — assert that mechanically by keeping old expectations as minimums
   (`got >= oldGolden`).
5. Add `TestTokenSetRatioNoRegression`: known-good match pairs (from the golden table) still pass
   the same `RankResults` `minScore` they passed before (anti-over-suppression: the happy path
   still matches). Add symmetry + empty-input cases.
6. Purely additive elsewhere: no signature changes, no caller edits, no threshold changes in
   scanner/itunes callers.
7. Bump headers on every touched file; keep existing guids.

## How to test

```bash
make ci
# caveat: staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
# you changed; the merge gate is Minimal CI green.
go test ./internal/matcher/ -v
```

## Acceptance criteria

- [ ] `grep -n "func TokenSetRatio" internal/matcher/fuzzy.go` hits
- [ ] Raise-only proven: `TestScoreMatchGolden` asserts `got >= oldGolden` for every pre-existing row
- [ ] `TestTokenSetRatioNoRegression` green — known-good pairs still pass their prior `minScore` (anti-over-suppression)
- [ ] Symmetry + empty-input (→0, non-matching, non-crashing) cases tested
- [ ] Tests green; vet/lint clean (`make ci`, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(matcher): token-set ratio similarity, raise-only blend into ScoreMatch (INIT-3-T6)

Pure-Levenshtein ScoreMatch punished token reordering ("Author: Title" vs
"Title - Author"). TokenSetRatio adds order-insensitive comparison built on
the existing normalize helper, blended raise-only so no previously-accepted
match can regress (golden-fixture enforced across scanner/itunes/matcher
callers).

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/metadata-matching-fuzzy-token-set
gh pr create --fill
gh pr merge <number> --rebase
```

(When running under a coordinated sweep, STOP after commit — the coordinator owns push/PR/merge.)

## Idempotency / Rollback

If `grep -n "func TokenSetRatio" internal/matcher/fuzzy.go` hits, this task is already applied —
run the acceptance checks instead of re-applying. Rollback = revert the commit; `ScoreMatch`
returns to the pure Levenshtein+heuristic blend, and because the blend was raise-only, no caller
behavior depends on the new signal existing.
