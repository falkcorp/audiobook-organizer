<!-- file: docs/agent-tasks/todo-completion/web/TASK-168-make-narrator-publisher-genre-and-release-year-f.md -->
<!-- version: 1.0.0 -->
<!-- guid: b0435971-2c0a-47b5-bd96-a81e6f1bedba -->
<!-- last-edited: 2026-08-21 -->

# TASK-168 — Make Narrator, Publisher, Genre, and Release Year fields link to filtered library views (all four have real filters behind them) (TODO.md L3164)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Unlike author_id/series_id (dedicated int params needing new plumbing), these four go through the EXISTING filters=JSON field-filter mechanism (buildFieldFilters already handles author/series/genre/language as text filters) — narrower, more reuse-driven change. · **Depends on:** none · **Wave:** 5

Source: `TODO.md` line 3164 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Narrator, publisher, genre, and release year.** " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-168-make-narrator-publisher-genre-and-release-year-f" -b agent/web-168-make-narrator-publisher-genre-and-release-year-f origin/main
cd "$REPO/.worktrees/web-168-make-narrator-publisher-genre-and-release-year-f"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In BookDetailInfoTab.tsx, turn Narrator, Publisher, Genre, and Release Year into real <a href> links targeting `/library?filters=[{\"field\":\"narrator\",\"value\":\"<name>\",\"negated\":false}]` (URL-encoded JSON, same shape buildFieldFilters in Library.tsx already produces for the genre/language filters that exist today), one link per narrator if multiple (book.narrators[] array, same multi-value pattern as authors[]). Release Year should filter on the 'year' field using book.audiobook_release_year (the field that most directly means 'released in Y').

## Background (verify before editing)

- genre and language already have an existing UI-driven filters= path via Library.tsx's buildFieldFilters (filters.genre, filters.language) — this task reuses that exact mechanism/shape rather than inventing a new URL scheme.
- narrator and publisher are new to the UI's filter state (buildFieldFilters currently only builds author/series/genre/language) but the backend field-filter matcher already supports them, so no backend work is needed.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n '\"narrator\":\|case \"narrator\"\|case \"genre\"\|case \"publisher\"\|case \"year\"' internal/audiobooks/service_filtering.go   # multiple hits including case statements at ~L440, L446, L450, L538 — narrator, genre, publisher, year are all implemented filter fields
  sed -n '647,656p' internal/audiobooks/service_filtering.go   # allFilterFieldNames includes 'narrator', 'genre', 'publisher', 'year' — all four names are in the canonical, matcher-pinned filter-field list
  ```

### Reuse — don't invent

- Use `buildFieldFilters (existing pattern for author/series/genre/language text filters)` in `web/src/pages/Library.tsx` (verify: `grep -n 'const buildFieldFilters' web/src/pages/Library.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. In BookDetailInfoTab.tsx, for Narrator: build one <Link> per entry in book.narrators[] (mirroring the multi-author pattern from todo_line 3156), hrefing to a URL-encoded `filters=[{\"field\":\"narrator\",\"value\":\"<name>\",\"negated\":false}]`.
2. For Publisher: single <Link> (publisher is not a multi-value field on the book payload) with the same filters=JSON shape, field='publisher'.
3. For Genre: single <Link>, field='genre', reusing the exact filter shape Library.tsx's buildFieldFilters already produces for filters.genre (read that code first to match value-formatting/casing conventions exactly).
4. For Release Year: <Link> with field='year', value=String(book.audiobook_release_year) — confirm via the 'year' case in service_filtering.go (L538) that a bare year string substring-matches correctly against both print and release year before committing to this value format.
5. Verify Library.tsx correctly parses an incoming ?filters=... URL param on page load for a field it did not itself generate (narrator/publisher are new) — Library.tsx must already read an incoming filters= param generically (check how it currently seeds filter UI state from the URL) since this is how tag/genre links would already need to round-trip; confirm rather than assume.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_168.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with no narrator, publisher, or genre must render plain text, not a link to an empty-value filter (an empty-value filter is explicitly rejected server-side per commit 27f386b2 'reject empty filter values instead of matching every book' — a link that produced one would 400).
- Multiple narrators must each link separately, same reasoning as multiple authors.

## Tests

- web/src/components/bookdetail/BookDetailInfoTab.test.tsx: one assertion per field (narrator, publisher, genre, release year) that the rendered link's filters= param decodes to the expected {field, value} object.
- web/src/pages/Library.test.tsx (or nearest existing test): assert landing on a URL with a narrator/publisher filters= param correctly narrows the displayed list.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] npm --prefix web run lint && npm --prefix web test passes.
- [ ] Each of the 4 fields renders as a real anchor, not an onClick-only element.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_168.md`.

## Commit message

```
feat(web): Make Narrator, Publisher, Genre, and Release Year fields lin (TODO L3164)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`npm --prefix web run lint && npm --prefix web test passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The item's own caution — 'check each has a real filter behind it before making it a link' — is satisfied by the verified_anchors above: all four are real, unlike version_group_id was before its recent fix (see todo_line 3168/3356). library_state and tags were named by the item as 'good candidates' with existing filter support but are not book-detail-page fields in scope here.
