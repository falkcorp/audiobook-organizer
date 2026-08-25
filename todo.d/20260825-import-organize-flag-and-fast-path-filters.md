- [x] **Decide whether `POST /import/file`'s `organize` flag should be wired or
      removed.** Decided 2026-08-25: **wired**, option (1), with the blast
      radius handled rather than accepted. The user made the call on the one
      sub-decision the code could not: the flag is honored on its own and is
      **not** ANDed with `auto_organize` (prod has `auto_organize=false`, so
      ANDing would have made an explicitly-ticked checkbox silently do nothing
      — this same bug wearing a different condition), and the checkbox now
      **defaults OFF** so no import moves files unless someone chose it.
      Honored by enqueueing `library.organize` with `BookIDs=[created.ID]`
      rather than calling `PerformOrganize` inline, so it inherits the op's
      ConcurrencyKey, cancellation, timeout and permission checks; the ID (not
      a path) satisfies the `os.Rename` warning below.

      ⚠️ **The wiring alone would have been INERT, and this is the part worth
      keeping.** `FilterBooksNeedingOrganization`
      (`internal/organizer/service.go:689-696`) drops any book whose `FilePath`
      is outside `RootDir` and which has **zero `book_files` rows**, counting
      it into `skippedMissingFiles` behind a `log.Debug`. An imported file is
      outside `RootDir` by definition — that is what importing means. And
      `internal/importer` created no `book_file` rows at all: `CreateBookFile`
      was not on `importBookStore`, so no call site could exist to look broken.
      An imported book therefore had a row, and audio on disk, and nothing
      connecting the two — which also means no route to playback, not just no
      organize. Fixed in the same PR. Verified the filter is on the live path
      (`PerformOrganize:334` calls it), and confirmed with another lane that
      this is a **separate defect** from the scanner-path `book_file`
      regression (that one has a hard Aug 14 boundary and an all-scan sample;
      this one is structural and presumably always existed).

      Lesson worth carrying: a feature can be inert because of a missing row
      three packages away, and every test that asserts "the op was enqueued"
      passes anyway. The original UI offering was:
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

- [x] **Prune the merged worktrees under `.worktrees/`.** Done 2026-08-25: 22 →
      6, each one content-verified with `git cherry origin/main HEAD` before
      removal rather than trusted on its PR being MERGED. That check earned its
      keep — `scan-cache-spec` held a commit that never landed despite #2868
      being merged (a rebase-merge silent drop, same as #2831), rescued as
      `6c54bb9d4`. Left in place: three worktrees with uncommitted work and two
      with real unmerged commits.

      The reason to keep the count low: `grep -rn` from the repo root descends
      into every worktree, so a search returns hits from many divergent
      snapshots with no signal as to which is live, and an agent told to
      "search the repo" cannot tell them apart. Agent instructions should say
      "verify against `origin/main`".

      ⚠️ **Correction to how this item was originally filed.** It claimed a bug
      had been reported against `internal/server/audiobooks_helpers.go`, "a file
      deleted by `faf755ffa` surviving only in stale worktrees". That is wrong
      in both halves: the file **exists** at `origin/main`, and `faf755ffa`
      **added** it. The error came from running `git log --diff-filter=AD` — A
      *or* D — seeing a single commit, and reading it as the D. `--diff-filter=A`
      alone shows it was an addition.

      The item's conclusion survives its evidence, but only partly, so the
      honest version is: one finding in that sweep was genuinely stale (it
      described code fixed nine commits earlier, from a local `main` that was
      nine commits behind), and the citation I dismissed as pointing into a
      graveyard was in fact a valid path I had not read. The lesson is narrower
      than first written and cuts both ways — a stale tree does corrupt agent
      findings, and so does a hasty refutation of one.
