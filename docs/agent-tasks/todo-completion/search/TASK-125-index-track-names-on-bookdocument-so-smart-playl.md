<!-- file: docs/agent-tasks/todo-completion/search/TASK-125-index-track-names-on-bookdocument-so-smart-playl.md -->
<!-- version: 1.0.0 -->
<!-- guid: f547581c-356b-4819-98b6-85e57efb577a -->
<!-- last-edited: 2026-08-21 -->

# TASK-125 — Index track names on BookDocument so smart playlists can match them (TODO.md L618)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · search subagent · **Why:** touches the index schema (mapping-version bump forces a full library reindex on next restart), a probe-derived narrow interface, and query-time field weighting -- needs care to keep interfacebloat-style narrowness · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 618 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🔍 **Index track names so smart playlists can match" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/search-125-index-track-names-on-bookdocument-so-smart-playl" -b agent/search-125-index-track-names-on-bookdocument-so-smart-playl origin/main
cd "$REPO/.worktrees/search-125-index-track-names-on-bookdocument-so-smart-playl"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a `TrackNames []string` field to BookDocument, register it as an analyzed multi-value text field in bookIndexMapping, populate it in BookToDoc from each book's BookFile.Title (falling back to the file's base name when Title is empty, mirroring loadChapters' own Title/Filename fallback in mapper.go:258-260), and bump bookMappingVersion so every deployed index is rebuilt to pick it up -- so a smart playlist / free-text search for a track name like 'Job' matches the book that contains it.

## Background (verify before editing)

- document.go:19-29: BookDocument today carries only book-level fields (title, author, narrator, series, publisher, description, file_path); no per-track data.
- index_builder.go:23-28 indexBuilderStore has exactly 4 methods (GetBookByID, GetAuthorByID, GetSeriesByID, GetBookTags) -- adding GetBookFiles(bookID) is a 5th narrow addition, still well under interfacebloat limits.
- mapper.go:256-264 already has the exact Title-vs-Filename fallback logic to mirror when building track names.
- bleve_index.go:94 bookMappingVersion="2" is the mechanism that forces Open() to delete and recreate the on-disk index (bleve_index.go:105-138) when the mapping changes; NOT bumping it means the new field is silently invisible on already-deployed indexes.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'TrackNames' internal/search/document.go   # 0 hits — BookDocument has no TrackNames field
  grep -n 'book.AddFieldMappingsAt' internal/search/bleve_index.go   # >=15 hits — bookIndexMapping registers each analyzed text field individually
  grep -n 'type indexBuilderStore interface' internal/search/index_builder.go   # 1 hit L23 — indexBuilderStore is the narrow 4-method probe-derived interface BookToDoc uses
  grep -n 'GetBookFiles(bookID string) (\[\]BookFile, error)' internal/database/iface_bookfile.go   # 1 hit L20 — GetBookFiles(bookID) exists on the database and returns Title+FilePath per file
  grep -n 'const bookMappingVersion' internal/search/bleve_index.go   # 1 hit L94, currently "2" — bookMappingVersion gates whether the on-disk index is rebuilt
  ```

### Reuse — don't invent

- Use `textAnalyzed() field-mapping helper` in `internal/search/bleve_index.go` (verify: `grep -n 'textAnalyzed := func' internal/search/bleve_index.go`) — do NOT write a parallel helper.
- Use `keyword field-mapping helper (for exact multi-value match, alternative to analyzed text)` in `internal/search/bleve_index.go` (verify: `grep -n 'keyword := func' internal/search/bleve_index.go`) — do NOT write a parallel helper.
- Use `database.BookFile.Title / .FilePath fields` in `internal/database/store.go` (verify: `grep -n 'Title *string' internal/database/store.go`) — do NOT write a parallel helper.

## Step-by-step

1. internal/search/document.go: add `TrackNames []string json:"track_names,omitempty"` to BookDocument, near the existing Tags []string field (document.go:33-35), with a doc comment describing it as analyzed multi-value text (one entry per BookFile.Title, or filepath.Base(FilePath) when Title is empty).
2. internal/search/bleve_index.go: inside bookIndexMapping(), add `trackNames := textAnalyzed()` and `book.AddFieldMappingsAt("track_names", trackNames)` near the existing tags/keyword registrations (bleve_index.go ~505-510). Add "track_names" to textFieldBoosts (bleve_index.go:427-433) with a modest boost (e.g. 1.0, same as publisher) so it participates in free-text search without dominating title matches.
3. internal/search/bleve_index.go: bump `const bookMappingVersion = "2"` to `"3"` (line 94).
4. internal/search/index_builder.go: add `GetBookFiles(bookID string) ([]database.BookFile, error)` to indexBuilderStore (index_builder.go:23-28).
5. internal/search/index_builder.go: inside BookToDoc, after the existing field population, call `store.GetBookFiles(book.ID)` and build `doc.TrackNames` as one entry per file: `f.Title` if non-empty else `filepath.Base(f.FilePath)`. Add `"path/filepath"` to imports. Treat a GetBookFiles error as best-effort (log/skip), matching the file's existing 'silently skipped' doc comment (index_builder.go:82).
6. Verify every caller of indexBuilderStore / BookToDoc's Store argument (server_search.go:87, server_search.go:115, search_coverage_test.go) is *database.PebbleStore or database.Store, which already implement GetBookFiles -- confirm with `grep -n 'GetBookFiles' internal/database/pebble_store_bookfiles.go`.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_search_125.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Book with zero BookFile rows (ebook-only or fully missing files): TrackNames stays nil/empty, no panic.
- GetBookFiles error (deleted book mid-index-build): BookToDoc must not fail the whole document build -- log and continue with TrackNames unset, matching the file's existing best-effort policy.
- A single-file book whose one file's Title equals the book Title exactly: acceptable duplication in the index, not a bug -- do not de-dup against doc.Title.

## Tests

- internal/search/index_builder_test.go (or new): TestBookToDoc_PopulatesTrackNames -- a book with 2 BookFiles (one with Title set, one with empty Title) yields doc.TrackNames with the Title for the first and filepath.Base for the second.
- internal/search/bleve_index_test.go (or new): TestSearch_MatchesOnTrackName -- index a book whose title/author/narrator do NOT contain 'Job' but one BookFile.Title == 'Job'; a free-text search for 'job' must return that book.
- internal/search/mapping_version_test.go: existing test pattern (readMappingMarker) should be exercised to confirm a v2 on-disk index gets recreated when bookMappingVersion becomes "3".

Anti-over-suppression test: `N/A -- this is additive indexing, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/search/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/search/... passes.
- [ ] grep -n 'TrackNames' internal/search/document.go returns 1 hit.
- [ ] grep -n 'bookMappingVersion = "3"' internal/search/bleve_index.go returns 1 hit.
- [ ] Anti-over-suppression test: `N/A -- this is additive indexing, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/search/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_search_125.md`.

## Commit message

```
feat(search): Index track names on BookDocument so smart playlists can mat (TODO L618)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/search/... passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Bumping bookMappingVersion forces every deployed index to rebuild on next restart (bleve_index.go:122-125 logs a warning and the reconciler drains the dirty set) -- call this out explicitly in the PR description since it is a one-time cost on the production instance, not a bug.
