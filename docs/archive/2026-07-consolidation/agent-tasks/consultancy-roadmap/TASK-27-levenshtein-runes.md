<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-27-levenshtein-runes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8843eb55-093b-4afa-85bc-f4e2bd909924 -->
<!-- last-edited: 2026-07-03 -->

# TASK-27 — Rune-based Levenshtein for non-ASCII titles (consultancy MATCH-9)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-27-levenshtein-runes" -b agent/cr-27-levenshtein-runes origin/main
cd "$REPO/.worktrees/cr-27-levenshtein-runes"
git rebase origin/main
```

## Goal

Fix consultancy finding MATCH-9: `LevenshteinDistance` in
`internal/matcher/fuzzy.go` indexes and measures strings as **bytes**, while
`normalize` deliberately preserves all Unicode letters/digits. Accented
(e.g. "Émile Zola") or CJK titles have characters that are 2-4 bytes each, so
a single substituted character counts as up to 4 edits, and `len()`-based
length ratios are byte-inflated. This skews fuzzy-match scores low and
asymmetric for non-ASCII titles/authors versus their ASCII-folded forms.
Convert the distance computation and its length-based callers to operate on
`[]rune`, not bytes.

## Background (verify before editing)

- `LevenshteinDistance(a, b string) int` in `internal/matcher/fuzzy.go`
  currently does `strings.ToLower`, then uses `len(a)`/`len(b)` (byte length)
  for the DP grid dimensions, and indexes `a[i-1]`/`b[j-1]` as raw bytes
  inside the double loop. Any multi-byte UTF-8 rune therefore contributes
  multiple "positions" to the DP matrix instead of one.
- `ScoreMatch(query, target string) int` calls `normalize()` first (which
  keeps all Unicode letters/digits/spaces — no ASCII-only assumption), then:
  - a substring-match length ratio: `ratio := float64(len(q)) / float64(len(t))`
    — byte length, same bias.
  - a whole-string fuzzy pass: `dist := LevenshteinDistance(q, t)` followed by
    `maxLen := max(len(q), len(t))` — byte length again, mismatched against
    rune-based `dist` if you fix `LevenshteinDistance` alone without also
    fixing this call site.
  - a per-word fuzzy pass: `dist := LevenshteinDistance(q, w)` followed by
    `wLen := max(len(q), len(w))` — same byte-length issue.
  - `normalize(s string) string` already iterates `for _, r := range s` (runes)
    and is unaffected — no change needed there.
- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func LevenshteinDistance\|func ScoreMatch\|func normalize\|ratio :=\|dist := LevenshteinDistance\|maxLen :=\|wLen :=" internal/matcher/fuzzy.go
  ```
  As of this writing (verified against a fresh checkout):
  - `func LevenshteinDistance` at line 19 (body through ~46).
  - `func ScoreMatch` at line 51.
  - substring ratio (`ratio := ...`) at line 77.
  - whole-string `dist :=` / `maxLen :=` at lines 92-93.
  - per-word `dist :=` / `wLen :=` at lines 105-106.
  - `func normalize` at line 140.
  If these don't match, trust the grep output over this brief.

## Step-by-step

1. In `internal/matcher/fuzzy.go`, rewrite `LevenshteinDistance` to convert
   both inputs to `[]rune` right after lowercasing (`ra := []rune(a)`,
   `rb := []rune(b)`), use `len(ra)`/`len(rb)` for the DP dimensions, and index
   `ra[i-1]`/`rb[j-1]` in the comparison — do not change the function
   signature (still `func LevenshteinDistance(a, b string) int`) or the
   single-row DP algorithm shape, only the byte-vs-rune indexing.
2. In `ScoreMatch`, fix the three length-ratio call sites so they use rune
   counts consistent with the (now rune-based) `LevenshteinDistance` result:
   - Substring ratio at ~line 77: replace `len(q)`/`len(t)` with
     `len([]rune(q))`/`len([]rune(t))` (or precompute rune slices once near
     the top of `ScoreMatch` and reuse them — either is fine, prefer whichever
     keeps the diff smallest).
   - Whole-string `maxLen` at ~line 93: replace `len(q)`/`len(t)` with rune
     counts.
   - Per-word `wLen` at ~line 106: replace `len(q)`/`len(w)` with rune counts.
   - Leave `strings.HasPrefix`, `strings.Contains`, and `strings.Fields`
     untouched — those are correctness-neutral for this bug (Go's UTF-8
     string operations already work correctly at the byte level for these;
     only length/index arithmetic was wrong).
3. Do not change `normalize` — it already iterates by rune and needs no fix.
4. Add test cases (extend `TestLevenshteinDistance` and/or `TestScoreMatch` in
   `internal/matcher/fuzzy_test.go`, matching the existing table-driven style)
   covering:
   - An accented pair, e.g. `LevenshteinDistance("Émile Zola", "Emile Zola")`
     — before the fix this counts as 2 edits (byte-level diff of "É" (2
     bytes) vs "E" (1 byte)); after the fix it must be exactly `1` (one rune
     substituted).
   - A CJK pair, e.g. `LevenshteinDistance("東京", "東京都")` (Tokyo vs Tokyo-to)
     — must be `1` (one rune inserted), not `3` (byte-inflated).
   - A mixed ASCII/non-ASCII pair to confirm no regression on plain ASCII
     inputs, e.g. `LevenshteinDistance("kitten", "sitting")` must still be `3`
     (this case already exists in the table — keep it passing).
   - A `ScoreMatch` case demonstrating the ratio fix doesn't crash or
     misbehave on non-ASCII input, e.g.
     `ScoreMatch("Zola", "Émile Zola")` returns a sane positive score (assert
     it's `> 0`, not an exact byte-sensitive value, to keep the test robust).
   Benchmarks are optional — do not add one unless it's trivial.
5. Bump the file header (version + `last-edited`) on every file you touch
   per `.standards/instructions/file-headers.md` — that's `internal/matcher/fuzzy.go`
   and `internal/matcher/fuzzy_test.go`.

## How to test

```bash
go build ./...
go test ./internal/matcher/... -run . -v -count=1
go vet ./internal/matcher/...
```

## Acceptance criteria

- [ ] `LevenshteinDistance` computes distance over `[]rune`, not bytes, for
      both inputs.
- [ ] `ScoreMatch`'s substring ratio, whole-string `maxLen`, and per-word
      `wLen` all use rune counts consistent with the rune-based distance.
- [ ] `LevenshteinDistance("Émile Zola", "Emile Zola") == 1` (not 2).
- [ ] A CJK insertion/deletion case (e.g. "東京" vs "東京都") returns `1`
      (not byte-inflated to 3).
- [ ] Existing ASCII cases (e.g. `"kitten"`/`"sitting"` == 3) still pass
      unchanged — no regression on ASCII-only libraries.
- [ ] `go test ./internal/matcher/...` is green; `go vet` is clean.
- [ ] File headers bumped on both changed files.

## Commit message

```
fix(matcher): compute Levenshtein distance over runes, not bytes (MATCH-9)

LevenshteinDistance indexed strings as bytes while normalize() preserves all
Unicode letters/digits, so accented and CJK titles counted each multi-byte
character as up to 4 edits, skewing fuzzy-match scores low and asymmetric for
non-ASCII titles/authors. Convert the DP loop and ScoreMatch's length ratios
to operate on rune counts.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-27-levenshtein-runes
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `LevenshteinDistance` already converts to `[]rune` before the DP loop (or
if `ScoreMatch`'s ratio/maxLen/wLen calculations already use rune counts),
this task is done — verify with
`grep -n "\[\]rune" internal/matcher/fuzzy.go` and confirm rune conversion
appears both inside `LevenshteinDistance` and at the three length-ratio call
sites in `ScoreMatch` (not just one or the other — a partial fix that
runifies the distance but leaves `maxLen`/`wLen`/`ratio` on byte `len()` is
still buggy, since rune-based distances compared against byte-based lengths
produce nonsensical similarity ratios). Rollback = revert the commit; this
change touches only distance/length arithmetic, no signatures, no callers
outside this file are affected (`ScoreMatch`, `RankResults` signatures
unchanged).
