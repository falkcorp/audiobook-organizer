- [ ] **Decide whether `POST /import/file`'s `organize` flag should be wired or
      removed.** The web UI offers "Organize into library after import" as a
      checkbox that **defaults to on** (`web/src/pages/Library.tsx:377`,
      `useState(true)`), sends it on every import including bulk ones
      (`Library.tsx:939` maps `api.importFile(path, importFileOrganize)` over
      every selected target), and the API client serialises it faithfully
      (`web/src/services/api.ts:2578-2582`). The server decodes it into
      `importer.ImportFileRequest.Organize` (`internal/importer/service.go:117`)
      via `ShouldBindJSON` at `internal/server/handlers/filesystem.go:357-363`.

      That field is then read **zero** times. `internal/importer` does not import
      `internal/organizer` at all — not a removed call, not a commented-out one.
      The user gets a 201 and a success toast; the file never moves.

      The "never built" shape matters for choosing the fix, because the sibling
      path at `internal/server/handlers/metadata/handler.go:1349` *does* honor
      `req.Organize`, and `deluge_discovery.go:95-97` explicitly passes
      `Organize: false` — both consistent with an author who believed this was
      wired. That is evidence about belief, not intent, so the code cannot pick
      between the two candidate fixes:

      1. **Wire it.** `PerformOrganize` is the canonical pipeline as of
         `06c3ba3fd`, so there is a correct thing to call. Blast radius is the
         reason this needs a decision and not a drive-by: every future import
         would begin moving files under `RootDir` on a ~48k-book production
         library, and the checkbox defaults to ON, so the change is opt-out
         rather than opt-in for existing users.
         ⚠️ `OrganizeOneBook` `os.Rename`s the book, so a `FilePath` captured
         before the call is stale after it — any deferred work must carry
         `book.ID`, not a path.
      2. **Remove the lie.** Drop the checkbox and the field, or have the API
         reject `organize: true` with a 400 saying import does not organize.
         Cheap, honest, and reversible if (1) is later wanted.

      Either is defensible; shipping neither is not, because today the UI
      promises an action the server silently declines to take.

- [ ] **Make the `has_file_errors` fast path honor the rest of the query, or
      refuse it.** `ListAudiobooks`
      (`internal/server/handlers/audiobooks/handler.go:349`) returns inside a
      fast path that parses `params`, `author_id` and `series_id` at :342-346
      and then uses **none** of them, while also ignoring `search`, the entire
      `filters` JSON payload, and any requested sort. It hand-paginates the raw
      `ListBooksWithFileErrors()` ID slice and reports `count` as the length of
      that unfiltered slice.

      This is reachable from the shipped UI, not a theoretical combination:
      `web/src/pages/Dashboard.tsx:463` navigates to
      `/library?has_file_errors=true`, and `useLibraryQuery.ts:265` sends
      `hasFileErrors` alongside whatever filters and search the user already had
      active. The response is 200 with plausible rows, so nothing surfaces —
      the user sees their filter chips still lit above a result set that
      ignored every one of them, and a total that belongs to a different query.

      The same shape repeats immediately below it: the quick-query fast path at
      :401 says in its own comment that it "Replicates the has_file_errors
      pattern", so `missing_covers` / `in_import_path` / `no_isbn` /
      `duplicates_flagged` need the same decision.

      A fix probably does **not** need a caller-visible contract change:
      `database.BookSummaryFilter` already carries `RestrictToIDs`, so the ID
      slice can be handed to the normal filtered pipeline instead of being
      hand-paginated, which restores search, filters, sort, pagination and an
      honest count in one move while keeping store pushdown. Confirm that
      `RestrictToIDs` is reachable from this handler before committing to it;
      the fallback is to reject the combination with a 400.

- [ ] **Prune the merged worktrees under `.worktrees/`.** There are ~21, several
      already merged, and they are not merely untidy — they actively corrupt
      investigation. `grep -rn` from the repo root descends into all of them, so
      a search returns hits from up to 22 divergent snapshots with no signal as
      to which is live. On 2026-08-25 this produced two false findings in one
      agent report: a bug was reported at
      `internal/server/audiobooks_helpers.go`, a file **deleted** by `faf755ffa`
      and surviving only in three stale worktrees, and a second finding
      described code fixed nine commits earlier. Both cited `file:line` anchors
      that resolved cleanly, which is exactly what makes the failure silent.
      Until this is cleaned, agent instructions should say "verify against
      `origin/main`" rather than "search the repo".
