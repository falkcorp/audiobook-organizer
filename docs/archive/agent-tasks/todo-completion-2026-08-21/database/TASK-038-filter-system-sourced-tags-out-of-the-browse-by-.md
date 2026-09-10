<!-- file: docs/agent-tasks/todo-completion/database/TASK-038-filter-system-sourced-tags-out-of-the-browse-by-.md -->
<!-- version: 1.1.0 -->
<!-- guid: 0c3440bd-1f9a-4a93-b4aa-3e0ece62781d -->
<!-- last-edited: 2026-09-02 -->

# TASK-038 — Filter system-sourced tags out of the Browse-by-Tag cloud (TODO.md L10526)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — service_tags.go:16-21 ListAllUserTags is still 'return svc.store.ListAllTags()' with no Source filter, though pebble_store_tags.go:44 persists Source. Recommendation: keep, lowest priority of this set - TODO itself classes it 'UX preference, not a bug'.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** requires a new source-aware aggregation over the book_tag: keyspace (not just tag_idx:), touching the Store interface · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10526 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Hide system-sourced tags from the Browse-by-Tag " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-14.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-038-filter-system-sourced-tags-out-of-the-browse-by-" -b agent/database-038-filter-system-sourced-tags-out-of-the-browse-by- origin/main
cd "$REPO/.worktrees/database-038-filter-system-sourced-tags-out-of-the-browse-by-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make the /tags endpoint (and hence TagCloud.tsx's Browse-by-Tag UI) exclude system-sourced tags. Add a source-aware tag listing: a new PebbleStore method that scans the book_tag:<bookID>:<tag> primary-key prefix (which carries BookTag.Source) instead of the source-blind tag_idx: index, aggregating counts only for Source=='user' (treating legacy empty Source as 'user', matching the existing convention at pebble_store_tags.go:160-162). This is source-of-truth-correct rather than a maintained system-namespace-prefix denylist.

## Background (verify before editing)

- service_tags.go ListAllUserTags is misleadingly named — it does NOT filter by user source today, it returns every tag.
- TagCloud.tsx renders whatever availableTags it's given, sourced (via web/src/hooks/useLibraryFilters.ts and web/src/services/api.ts's listAllUserTags()) from GET /tags → ListAllUserTags handler → this same unfiltered store call.
- FilterSidebar.tsx also consumes the same availableTags prop, so a store-level fix also cleans that view — confirm with the coordinator whether FilterSidebar's filter dropdown should ALSO exclude system tags, or whether a component-level filter in TagCloud.tsx alone is preferred to keep FilterSidebar's advanced/power-user filtering unrestricted.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func (svc \*AudiobookService) ListAllUserTags" -A5 internal/audiobooks/service_tags.go   # 1 hit ~L16, body is 'return svc.store.ListAllTags()' — ListAllUserTags just forwards to store.ListAllTags with no source filter
  grep -n "tag_idx:" internal/database/pebble_store_tags.go   # ≥1 hit ~L223 — ListAllTags's tag_idx: index carries no source, so cannot distinguish system vs user tags today
  grep -n "metadata:source:" internal/database/tag_helpers.go   # 1 hit ~L21 — System tags use colon namespaces like metadata:source:<name>
  grep -n "Source:" internal/database/pebble_store_tags.go   # ≥1 hit ~L44 — book_tag: primary records DO carry a Source field ('user' vs 'system')
  ```

### Reuse — don't invent

- Use `BookTag.Source field + book_tag: primary key prefix scan, already used by GetBookTagsDetailed` in `internal/database/pebble_store_tags.go` (verify: `grep -n "func (p \*PebbleStore) GetBookTagsDetailed" internal/database/pebble_store_tags.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add a new PebbleStore method e.g. `ListAllUserSourcedTags() ([]TagWithCount, error)` in internal/database/pebble_store_tags.go that iterates the `book_tag:` prefix (not `tag_idx:`), unmarshals each BookTag, skips entries where Source not in {'', 'user'}, and aggregates per-tag counts (mirror the counting/sort logic already in ListAllTags at pebble_store_tags.go:222-253).
2. Add the method to the relevant Store interface (internal/database/iface_tags.go) and to internal/database/mock_store.go's MockStore (with a ...Func field following the existing ListAllTagsFunc pattern at mock_store.go:2343).
3. Update internal/audiobooks/service_tags.go's ListAllUserTags to call the new store method instead of store.ListAllTags().
4. Regenerate internal/database/mocks/mock_store.go (mockery-generated) for the new interface method — check the Makefile for the mock-generation target rather than hand-editing.
5. Run `make ci` and fix any interface-conformance compile errors in other Store implementations.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_038.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A tag applied by BOTH a user and the system on different books (same tag string, different Source per book) — count should reflect only the user-sourced occurrences, not the system ones, even for the same tag text.
- Empty library (no tags at all) must return an empty slice, not an error.

## Tests

- internal/database/pebble_store_tags_test.go (or a new _test.go beside it): TestListAllUserSourcedTags_ExcludesSystemTags — write one user tag and one tag with Source='system' on the same book, assert only the user tag appears in the result with count 1.
- internal/database/pebble_store_tags_test.go: TestListAllUserSourcedTags_LegacyEmptySourceTreatedAsUser — a BookTag with Source=='' (pre-migration legacy row) must still be included, matching the existing convention.
- internal/audiobooks/service_tags_test.go: assert ListAllUserTags calls the new store method (via a mock) rather than the old ListAllTags.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/database/... ./internal/database/mocks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/database/... -run TestListAllUserSourcedTags` passes.
- [ ] A manual check: create a book with tag 'metadata:source:audible' via AddBookTagWithSource(id, tag, "system") and a user tag 'my-favorite' via the normal user-tag path; GET /tags must list only 'my-favorite'.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/database/... ./internal/database/mocks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_038.md`.

## Commit message

```
refactor(database): Filter system-sourced tags out of the Browse-by-Tag cloud (TODO L10526)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Confirm with the coordinator whether FilterSidebar.tsx's tag filter dropdown should also switch to the new user-only endpoint, or intentionally keep showing system tags there for power users — the TODO item only names the 'Browse-by-Tag cloud' (TagCloud.tsx) explicitly.
