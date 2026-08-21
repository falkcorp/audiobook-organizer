<!-- file: docs/agent-tasks/todo-completion/search/TASK-135-surface-to-the-user-when-all-and-or-any-stopword.md -->
<!-- version: 1.0.0 -->
<!-- guid: efc6dd76-3977-490d-88e1-0d720a960213 -->
<!-- last-edited: 2026-08-21 -->

# TASK-135 — Surface to the user when 'all'/'and' (or any stopword) is silently dropped from a search query (TODO.md L3369)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · search subagent · **Why:** Requires threading a new signal (which terms were dropped) from the translator through the search handler response and into the UI — a small new data channel, not just a logic tweak. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 3369 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`all` and `and` are stopwords and are silently d" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/search-135-surface-to-the-user-when-all-and-or-any-stopword" -b agent/search-135-surface-to-the-user-when-all-and-or-any-stopword origin/main
cd "$REPO/.worktrees/search-135-surface-to-the-user-when-all-and-or-any-stopword"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change dropStopwordOnlyConjuncts (or its caller at bleve_translator.go:89) to also return the list of dropped term strings, thread that list through to the search response DTO as e.g. `dropped_terms: ["all", "and"]`, and have the Library search UI show a small inline note ('some common words were ignored in your search: all, and') when the list is non-empty. Do NOT change the dropping behavior itself — it exists specifically to fix the 'shards of oblivion' all-stopword-query bug and must keep working exactly as-is for that case.

## Background (verify before editing)

- Measured via TestReproAllJobsAndClasses: 'All Jobs and Classes' searches only 'Jobs AND Classes'; 'all jobs' searches only 'jobs' — confirmed still current at HEAD.
- Independent of the index-coverage bug fixed on 2026-08-13 (todo_line 3423/3433/3384) and independent of the quoted-phrase bug already fixed for todo_line 3377 — needs its own, separate change.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func dropStopwordOnlyConjuncts' internal/search/bleve_translator.go   # 1 hit, with the function called at L89 from the conjunct-building path — the stopword-dropping function exists and behaves as described
  grep -rn 'stopword\|dropped' internal/server/handlers/*.go web/src/pages/Library.tsx 2>/dev/null | grep -i 'ignored\|discard\|stopword'   # 0 hits — nothing surfaces the drop to the user today
  ```

### Reuse — don't invent

- Use `dropStopwordOnlyConjuncts` in `internal/search/bleve_translator.go` (verify: `grep -n 'func dropStopwordOnlyConjuncts' internal/search/bleve_translator.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/search/bleve_translator.go, change dropStopwordOnlyConjuncts (line 166) to return `([]Node, []string)` — the kept nodes plus the values of the dropped FreeTextNodes — instead of just `[]Node`.
2. Update its call site at line 89 to collect the dropped-terms slice and propagate it up through whatever function builds the final Bleve query (likely the same function containing line 89).
3. Thread the dropped-terms list into the search response struct in internal/server/handlers/search.go (or wherever the search handler builds its JSON response) as a new `dropped_terms []string` (omitempty) field.
4. In the web Library search UI (grep -rn 'search' web/src/pages/Library.tsx for the search-results rendering block), read `dropped_terms` from the API response and render a small Alert/note when non-empty, listing the dropped words.
5. Add a test asserting the API response for 'All Jobs and Classes' includes dropped_terms: ['All', 'and'] (or however TestReproAllJobsAndClasses's fixture capitalizes them) alongside the narrowed results.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_search_135.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- An all-stopword query (the original 'shards of oblivion' case dropStopwordOnlyConjuncts protects) returns children unchanged (dropped==0 || len(kept)==0 short-circuit at L176) — in that case there ARE no dropped terms to surface for THIS conjunct, since the function deliberately keeps everything rather than dropping to empty; confirm the surfacing logic doesn't misreport in this specific branch.
- OR-disjunction stopwords are deliberately left alone per the existing comment ('Conjunction only... OR is deliberately left alone') — do not surface a false 'dropped' note for terms that were never actually dropped from a disjunction.

## Tests

- internal/search/bleve_translator_test.go: assert dropStopwordOnlyConjuncts returns both the kept nodes and the correct dropped-terms list for the 'All Jobs and Classes' case.
- internal/server/handlers/search_test.go: assert the API response includes dropped_terms for a query containing stopwords, and omits/empties it for a query with none.
- web/src/pages/Library.test.tsx: assert the UI note renders when dropped_terms is present.
- Anti-over-suppression: TestReproAllJobsAndClasses itself (or an equivalent) must still show the query narrows to 'Jobs AND Classes' — the dropping behavior itself must not change, only its visibility.

Anti-over-suppression test: `TestReproAllJobsAndClasses (or equivalent) still passes, confirming the underlying stopword-drop behavior is unchanged.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] make ci (Go) and npm --prefix web test both pass.
- [ ] A manual search for 'all jobs' shows both the narrowed results AND a visible note that 'all' was ignored.
- [ ] Anti-over-suppression test: `TestReproAllJobsAndClasses (or equivalent) still passes, confirming the underlying stopword-drop behavior is unchanged.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_search_135.md`.

## Commit message

```
feat(search): Surface to the user when 'all'/'and' (or any stopword) is si (TODO L3369)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`make ci (Go) and npm --prefix web test both pass.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Distinct from and unrelated to the (already-fixed) quoted-phrase bug at todo_line 3377 — do not conflate the two when scoping this fix.
