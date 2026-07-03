<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-25-isgarbagevalue-substring.md -->
<!-- version: 1.0.0 -->
<!-- guid: f5ac3df8-9d37-4ff2-954c-599e0839b139 -->
<!-- last-edited: 2026-07-03 -->

# TASK-25 — `IsGarbageValue` "error" substring false positives (consultancy MATCH-3)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku · **Wave:** 2 · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-25-isgarbagevalue-substring" -b agent/cr-25-isgarbagevalue-substring origin/main
cd "$REPO/.worktrees/cr-25-isgarbagevalue-substring"
git rebase origin/main
```

## Goal

Fix `IsGarbageValue` in `internal/metafetch/service_scoring.go` so it no longer
rejects legitimate titles/authors that merely contain the substring "error"
(or other marker words) — e.g. "The Terror", "The Comedy of Errors",
"Terrorbyte" — while still rejecting genuine garbage values ("unknown",
empty-ish strings) and genuine HTML/error-page leaks ("403 Forbidden",
"internal server error", raw HTML fragments from a failed fetch).

## Background (verify before editing)

- `IsGarbageValue(s string) bool` lives in `internal/metafetch/service_scoring.go`.
  It lower-cases and trims the input, then does an exact-match check against a
  blocklist (`"unknown"`, `"narrator"`, `"various"`, `"n/a"`, `"none"`,
  `"null"`, `"undefined"`, `""`, `"test"`, `"untitled"`, `"no title"`,
  `"no author"`, `"various authors"`, `"various artists"`) — that part is
  already anchored to the *whole trimmed value* and is fine, do not change it.
- The bug is the second block: it does `strings.Contains(lower, "error")` (plus
  `"<html"`, `"<!doctype"`, `"403 forbidden"`) against the **whole string**,
  so any legitimate title/author containing "error" as a substring —
  "The Terror", "The Comedy of Errors", "Terrorbyte", "Erroll Garner" — is
  incorrectly classified as garbage.
- `IsGarbageValue` gates real behavior downstream, not just this one function:
  - `IsBetterValue` / `IsBetterStringPtr` (same file) call `IsGarbageValue` to
    decide whether to accept a new metadata value during apply — a false
    positive here permanently blocks writing a real title/author.
  - `hintsFromBook` (same file) calls `IsGarbageValue` to clean transcribed
    title/author/narrator before transcription-boost scoring — a false
    positive silently disables the transcription boost for that book.
  - `service_search.go` calls `IsGarbageValue(bookAuthor)` and
    `IsGarbageValue(bookNarrator)` to decide whether to blank those fields
    before search — a false positive loses the author/narrator boost for that
    search.
  - `service_fetch.go` and `service_apply.go` also call `IsGarbageValue` at
    several points to gate whether an existing/incoming value is usable.
  - None of these callers need to change — fixing `IsGarbageValue` itself
    fixes all of them, since it's the single source of truth.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func IsGarbageValue\|func IsBetterValue\|func IsBetterStringPtr\|func hintsFromBook" internal/metafetch/service_scoring.go
  grep -rn "IsGarbageValue(" internal/metafetch/*.go
  ```
  Confirm the current body of `IsGarbageValue` matches what's described above
  (exact-match blocklist loop, then a `strings.Contains` block for HTML/error
  patterns) before editing — read the full function, do not assume the line
  numbers in this brief are still correct.

## Step-by-step

1. Open `internal/metafetch/service_scoring.go`, locate `IsGarbageValue`
   (re-verify with the grep above).
2. Keep the existing exact-match blocklist loop over the trimmed, lowercased
   full value unchanged.
3. Replace the `strings.Contains(lower, "error")` check with **anchored**
   checks that can't match inside a real title/author:
   - Keep `strings.Contains(lower, "<html")` and
     `strings.Contains(lower, "<!doctype")` — these are safe; no real title
     contains a literal `<html` or `<!doctype` tag.
   - Keep `strings.Contains(lower, "403 forbidden")` — safe for the same
     reason (and add other literal HTTP-status phrases you find worth
     covering, e.g. `"404 not found"`, `"500 internal server error"`, if you
     want extra coverage — optional, not required for the acceptance
     criteria).
   - Replace the bare `strings.Contains(lower, "error")` with prefix/anchored
     checks for actual error-message shapes only, such as:
     - `strings.HasPrefix(lower, "error:")` (or `"error :"` with a space
       before the colon, if useful)
     - `strings.Contains(lower, "http error")`
     - `strings.Contains(lower, "internal server error")`
     - Do **not** add a bare `strings.Contains(lower, "error")` anywhere —
       that is the exact defect being fixed.
4. Update `internal/metafetch/service_test.go`'s `TestIsGarbageValue` table:
   - The existing case `{"some error occurred", true}` is testing the buggy
     behavior — change it to `{"some error occurred", false}` (a sentence
     merely containing "error" is not an error-page leak and must now pass
     through as a legitimate, non-garbage string).
   - Add new table-driven cases that must return `false` (legitimate values
     containing the marker substring):
     - `{"The Terror", false}`
     - `{"The Comedy of Errors", false}`
     - `{"Terrorbyte", false}`
     - `{"Erroll Garner", false}`
   - Add new table-driven cases that must still return `true` (genuine
     garbage/error-page leaks):
     - `{"Error: connection refused", true}`
     - `{"HTTP Error 500", true}` (via the `"http error"` check — adjust
       casing/wording to match whatever anchored pattern you implement, and
       make sure the test case actually exercises it)
     - Keep `{"<html>something</html>", true}`, `{"<!DOCTYPE html>", true}`,
       `{"403 Forbidden", true}` as-is — these already pass and must keep
       passing.
5. Grep for any other place in the repo that special-cases the string
   `"error"` inside a garbage/value-quality check (not general error
   handling) to make sure you're not leaving a duplicate bare-substring check
   elsewhere:
   ```bash
   grep -rn 'Contains(lower, "error"\|Contains(.*"error")' internal/metafetch/
   ```
   If this task's target function is the only hit, no further changes are
   needed.
6. Bump the file header (version bump + `last-edited` date) on every file you
   touch, per `.standards/instructions/file-headers.md` — at minimum
   `internal/metafetch/service_scoring.go` and
   `internal/metafetch/service_test.go`.

## How to test

```bash
go build ./...
go test ./internal/metafetch/... -run TestIsGarbageValue -v -count=1
go test ./internal/metafetch/... -count=1
go vet ./internal/metafetch/...
```

## Acceptance criteria

- [ ] `IsGarbageValue("The Terror")`, `IsGarbageValue("The Comedy of Errors")`,
      `IsGarbageValue("Terrorbyte")`, and `IsGarbageValue("Erroll Garner")` all
      return `false`.
- [ ] Genuine garbage/error-leak values (`"unknown"`, `"<html>...</html>"`,
      `"<!DOCTYPE html>"`, `"403 Forbidden"`, an anchored error-message shape
      like `"Error: connection refused"`) still return `true`.
- [ ] No bare `strings.Contains(lower, "error")` (or equivalent) remains in
      `IsGarbageValue`.
- [ ] `TestIsGarbageValue` in `internal/metafetch/service_test.go` is updated
      to cover both the false-positive fix and the still-must-reject cases,
      and passes.
- [ ] `go test ./internal/metafetch/...` is green; `go build ./...` and
      `go vet ./internal/metafetch/...` are clean.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(metafetch): stop IsGarbageValue rejecting titles/authors containing "error" (MATCH-3)

IsGarbageValue used a bare substring match on "error", so legitimate titles
and authors like "The Terror" or "The Comedy of Errors" were misclassified as
garbage — blocking metadata writes, transcription-boost hints, and
author/narrator search boosts for any affected book. Replace the bare
substring check with anchored error-message-shape checks (prefix "error:",
"http error", "internal server error") that can't match inside a real title.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-25-isgarbagevalue-substring
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `IsGarbageValue` no longer contains a bare `strings.Contains(lower, "error")`
call (verify with
`grep -n 'Contains(lower, "error")' internal/metafetch/service_scoring.go`),
this task is already done — confirm the anchored checks exist and the test
table covers the "The Terror" / "Comedy of Errors" cases, then stop.
Rollback = revert the commit; the exact-match blocklist loop and the
`<html`/`<!doctype`/`403 forbidden` checks are untouched by this change and
remain in effect either way.
