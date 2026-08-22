<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-212-add-get-api-libraries-libraryid-series-seriesid-.md -->
<!-- version: 1.0.0 -->
<!-- guid: bbeb0427-66ff-4ef8-b76e-7867f3512eb8 -->
<!-- last-edited: 2026-08-21 -->

# TASK-212 — Add GET /api/libraries/:libraryId/series/:seriesId to the ABS surface (TODO.md L476)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** single-item variant of an existing well-documented handler (LibrarySeries) in the same file -- needs care reusing the right helpers and matching the exact per-series JSON shape but is not architecturally novel · **Depends on:** none · **Wave:** 3

Source: `TODO.md` line 476 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Series DETAIL is still not served.**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-212-add-get-api-libraries-libraryid-series-seriesid-" -b agent/server-handlers-212-add-get-api-libraries-libraryid-series-seriesid- origin/main
cd "$REPO/.worktrees/server-handlers-212-add-get-api-libraries-libraryid-series-seriesid-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Implement GET /api/libraries/:libraryId/series/:seriesId on the ABS-compatible surface, matching the per-series object shape that LibrarySeries (internal/server/handlers/abs/browse.go:493) already emits inside its `results` array (id, name, nameIgnorePrefix, libraryId, addedAt, updatedAt, books, totalDuration, numBooks), but for exactly one series returned as a bare JSON object (not wrapped in the list's pageResponse{Results,Total,Limit,Page} envelope). Register the new handler in Handler.Register (internal/server/handlers/abs/handler.go, next to line 465's LibrarySeries registration) and add the route string to absRouteList() (internal/server/wire_abs_routes.go, next to line 619's series entry) so the startup log and TestUnimplementedABSNamespacesAre404NotRedirect-style route-inventory tests stay accurate. No entry is needed in absCollisionDetailRoutes/absAppAPICollisions since /api/libraries/ is already unconditionally reserved.

## Background (verify before editing)

- LibrarySeries (browse.go:493) is the existing GET /api/libraries/:libraryId/series handler; it builds the same per-series gin.H shape this new handler needs to reuse for a single series, documented at browse.go:606-632.
- The identical defect (a resource's LIST route existed but its DETAIL route did not, so opening one 301'd into the app API and rendered empty) was already fixed once for playlists -- see the comment block at wire_abs_routes.go:99-125 describing the exact symptom ("opening a playlist... 301'd into /api/v1/playlists/:id and answered a shape the client cannot parse") and its fix (GET /api/playlists/:id added to absCollisionDetailRoutes because /api/playlists/ has a live /api/v1 twin).
- Series does NOT need the absCollisionDetailRoutes treatment playlists needed, because /api/libraries/ is already in absReservedPathPrefixes (wire_abs_routes.go:68) -- the TODO item itself calls this out: 'sits under the already-reserved /api/libraries/ prefix and therefore needs no routing decision at all'.
- GetSeriesByIDs(ids []int) (map[int]*database.Series, error) is already declared on the ABS library interface (handler.go:164) and implemented by the fake test double (library_fake_test.go:373), but nothing in browse.go currently calls it -- LibrarySeries instead calls GetAllSeries() and works over the whole set. The detail handler should call GetSeriesByIDs([]int{id}) to avoid an O(all series) scan for a single lookup.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "libraries/:libraryId/series" internal/server/handlers/abs/handler.go   # 1 hit, L465: r.GET("/api/libraries/:libraryId/series", auth, h.LibrarySeries) — only the series LIST route is registered today, not a detail route
  grep -n "\"/api/libraries/\"" internal/server/wire_abs_routes.go   # 1 hit, L68 inside absReservedPathPrefixes — /api/libraries/ is unconditionally reserved from the /api -> /api/v1 compat redirect, so no routing decision is needed to add a sub-path here
  grep -n "GET /api/playlists/:id" internal/server/wire_abs_routes.go   # 2 hits (absRouteList doc entry L624 + absCollisionDetailRoutes L142), with a comment block above explaining playlists opened empty until this route was added — the exact same detail-route-missing defect was already fixed once for playlists, establishing the precedent pattern this item follows
  grep -n "GetSeriesByIDs" internal/server/handlers/abs/handler.go internal/server/handlers/abs/browse.go   # 1 hit only, in handler.go:164 (interface declaration) -- 0 hits in browse.go confirms it is not yet called there — GetSeriesByIDs already exists on the library interface but is unused in browse.go, making it the natural fetch-one-series method to reuse instead of GetAllSeries+filter
  grep -n 'libraries/:libraryId/series' internal/server/handlers/abs/handler.go   # 1 hit at L465: r.GET("/api/libraries/:libraryId/series", auth, h.LibrarySeries) — only the series LIST route is registered today, not a detail route
  grep -n '"/api/libraries/"' internal/server/wire_abs_routes.go   # 1 hit at L68 inside absReservedPathPrefixes — /api/libraries/ is already unconditionally reserved, so no absCollisionDetailRoutes entry is needed
  grep -n 'Pattern: "/api/playlists/:id"' internal/server/wire_abs_routes.go   # 1 hit at L142 inside absCollisionDetailRoutes (which starts at L141) — the same detail-route-missing defect was already fixed for playlists, which DID need a collision entry because /api/playlists/ has a live /api/v1 twin
  grep -n 'GetSeriesByIDs' internal/server/handlers/abs/handler.go internal/server/handlers/abs/browse.go   # 1 hit only, handler.go:164 (interface declaration); 0 hits in browse.go — GetSeriesByIDs exists on the ABS library interface but is not yet called in browse.go, making it the fetch-one method to reuse
  ```

### Reuse — don't invent

- Use `h.knownLibrary(c)` in `internal/server/handlers/abs/browse.go` (verify: `grep -n "func (h \*Handler) knownLibrary" internal/server/handlers/abs/browse.go`) — do NOT write a parallel helper.
- Use `h.seriesBooksCached() / h.seriesPageBooks(...)` in `internal/server/handlers/abs/browse.go` (verify: `grep -n "func (h \*Handler) seriesBooksCached\|func (h \*Handler) seriesPageBooks" internal/server/handlers/abs/browse.go`) — do NOT write a parallel helper.
- Use `h.library.GetSeriesByIDs(ids []int)` in `internal/server/handlers/abs/handler.go` (verify: `grep -n "GetSeriesByIDs" internal/server/handlers/abs/handler.go`) — do NOT write a parallel helper.
- Use `ignorePrefix / msEpoch helpers already used by LibrarySeries` in `internal/server/handlers/abs/browse.go` (verify: `grep -n "func ignorePrefix\|func msEpoch" internal/server/handlers/abs/browse.go internal/server/handlers/abs/*.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/handlers/abs/browse.go, add a new method after LibrarySeries returns (~L649): `func (h *Handler) LibrarySeriesDetail(c *gin.Context)`.
2. Call `if !h.knownLibrary(c) { return }` first (browse.go:117), the same guard every sibling handler uses.
3. Parse `seriesID, err := strconv.Atoi(c.Param("seriesId"))`; on error respondError(c, http.StatusNotFound, "series not found") and return - series IDs are numeric internally, so a malformed id is indistinguishable from unknown.
4. Call `byID, err := h.library.GetSeriesByIDs([]int{seriesID})` (declared internal/server/handlers/abs/handler.go:164); on err respondError 500 'could not load series'; if byID has no entry for seriesID respondError 404 'series not found'.
5. Get `bySeries, berr := h.seriesBooksCached()` (browse.go:759, called by LibrarySeries at browse.go:521) and `enriched := h.seriesPageBooks(c.Request.Context(), []database.Series{*byID[seriesID]}, bySeries)` (browse.go:663, called by LibrarySeries at browse.go:597). seriesPageBooks returns map[int][]any and gives you ONLY the books.
6. Derive the other two numbers exactly as LibrarySeries does at browse.go:600-601 and browse.go:631: `built := bySeries[seriesID]`, `items, total := enriched[seriesID], built.totalDuration`, `if items == nil { items = []any{} }`, and numBooks = len(items). Do NOT take numBooks from a store count - browse.go:620-630 documents that as a measured production bug (9 of 50 series claimed numBooks >= 1 while carrying books: []). totalDuration must stay an int, never a float (browse.go:608-610).
7. Return the single object with respondJSON(c, http.StatusOK, gin.H{...}) carrying the same field set LibrarySeries emits per series (browse.go:606-632): id via strconv.Itoa, name, nameIgnorePrefix via ignorePrefix (internal/server/handlers/abs/mapper.go:487), libraryId via h.libraryID(), addedAt/updatedAt via msEpoch (internal/server/handlers/abs/dto.go:36), books, totalDuration, numBooks - NOT wrapped in pageResponse.
8. In internal/server/handlers/abs/handler.go, add the registration directly after L465: `r.GET("/api/libraries/:libraryId/series/:seriesId", auth, h.LibrarySeriesDetail)`.
9. In internal/server/wire_abs_routes.go, add `"GET /api/libraries/:libraryId/series/:seriesId",` directly after L619 inside absRouteList().

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_212.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- seriesId path param present but not a valid integer: 404, not 500 (strconv.Atoi error path)
- seriesId valid integer but library has no such series: 404 via the GetSeriesByIDs empty-map-entry check
- series exists but has zero books: numBooks/totalDuration must be 0 and books must be [] (not null), matching the existing LibrarySeries convention at browse.go:603-605 (`if items == nil { items = []any{} }`)
- unknown libraryId in the path: rejected by h.knownLibrary(c) before any series lookup runs, same as every other handler in this file

## Tests

- internal/server/handlers/abs/series_detail_test.go (new file): TestLibrarySeriesDetail_ReturnsMatchingSeriesFields -- seed one series with 2 books via the same oracle-seed helpers series_books_test.go uses (absSeedTwoSeries pattern at series_books_test.go:29), GET /api/libraries/:id/series/:seriesId, assert the JSON object (not array) has id/name/books/numBooks/totalDuration matching what LibrarySeries returns for the same series in its results array (cross-check against the LIST route's entry for that id -- same style as TestLibrarySeries_BooksAreLibraryItems at series_books_test.go:211).
- TestLibrarySeriesDetail_UnknownSeriesID404s -- GET a numeric id that does not exist, assert 404 with a body, not a redirect and not 200-with-empty.
- TestLibrarySeriesDetail_NonNumericSeriesID404s -- GET /api/libraries/:id/series/not-a-number, assert 404 (not 500) -- this is the anti-over-suppression check: a malformed id must not be silently coerced to 0 or panic strconv.Atoi unchecked.

Anti-over-suppression test: `TestLibrarySeriesDetail_NonNumericSeriesID404s` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] curl (or httptest) GET /api/libraries/1/series/<real-id> with a valid auth token returns 200 and a JSON object (not an array) with the same numBooks/totalDuration a corresponding LibrarySeries list entry shows for that id
- [ ] go test ./internal/server/handlers/abs/... -run TestLibrarySeriesDetail -count=1 -v passes
- [ ] grep -n 'LibrarySeriesDetail' internal/server/handlers/abs/handler.go shows exactly one registration line
- [ ] Anti-over-suppression test: `TestLibrarySeriesDetail_NonNumericSeriesID404s` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_212.md`.

## Commit message

```
feat(server-handlers): Add GET /api/libraries/:libraryId/series/:seriesId to the AB (TODO L476)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `curl (or httptest) GET /api/libraries/1/series/<real-id> with a valid auth token returns 200 and a JSON object (not an array) with the same numBooks/totalDuration a corresponding LibrarySeries list entry shows for that id` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Prefer GET /api/libraries/:libraryId/series/:seriesId over a bare /api/series/:id shim, per the TODO's own guidance and matching the upstream ABS API shape -- do not also add /api/series/:id in the same change, that would reopen the routing-decision question the TODO explicitly says to avoid. review_critical=false because this is a read-only ABS-compat GET with no write path.
