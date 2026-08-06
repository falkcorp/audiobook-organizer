<!-- file: docs/plans/2026-08-05-playlists-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: 29e3bb09-815f-46b7-9ef8-009c5e4595fd -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** This document was authored by
> an agent on 2026-08-05 but the adversarial verification pass (which grep-verifies
> that every cited function, struct field, op ID and file path actually exists) did
> NOT run — the workflow was halted by API rate limiting before stage 2.
>
> **Treat every code citation as a claim, not a fact.** The most common failure mode
> in generated plans is a confidently-cited symbol that does not exist. Verify before
> executing. The design reasoning and the measured production numbers are still sound
> and were drawn from real observations; the code references are what needs checking.


# Playlists — implementation plan

Design: [`docs/specs/2026-08-05-playlists-design.md`](../specs/2026-08-05-playlists-design.md)
Base: `main` @ `8c39469a`
Branch: `feat/playlists-file-level-entries`

> **Worktree discipline (CLAUDE.md, non-negotiable).** Before any edit:
> ```bash
> git -C /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer worktree list
> git -C /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer worktree add \
>     /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/playlists \
>     -b feat/playlists-file-level-entries
> ```
> All paths below are relative to that worktree root. Every file created or
> modified gets a version header (`// file:` / `// version:` / `// guid:` /
> `// last-edited:` for Go; `<!-- ... -->` for everything else) and a version
> bump on every change.

---

## 0. Sequencing rationale

Steps 1-8 have **no dependency on the ABS oracle** and can all land first.
**Step 9 is intentionally empty** — the scan-time hook it once held was dropped
(design §8 non-goal 11); the number is kept so the "step 10 / steps 11-12 /
step 14.x" references in this file and in the design stay valid.
Step 10 is a hard gate: **nothing in 11-12 may be written before a real ABS
playlist fixture exists** (design §6.2). Step 13 is the prod dry-run gate;
step 14 is the only apply.

Each step below is one commit and one reviewable diff. Steps 1-3 must land in
order (later steps compile against them); 4-8 are independent of each other.
**No step in this plan modifies any file under `internal/scanner/`** (step 9).

---

## Step 1 — `PlaylistEntry` type + store interface (no implementation)

**Intent:** land the types and the interface so every later step compiles
against a stable surface, and so the mock churn happens once.

| File | Intent |
|---|---|
| `internal/database/store.go` | **modify.** Add `PlaylistEntry`, `EntryTiming`, `EntryState` + its four constants, immediately after `UserPlaylist` (currently ends line 417) and before `PlaylistItem` (line 426). Add the nine provenance/counter fields to `UserPlaylist` (design §3.2). Do **not** add an `Entries` field — that is the whole point of D1; put a comment saying so, citing `pebble_store_playlists.go:311`. |
| `internal/database/iface_misc.go` | **modify.** Add the six new methods to `UserPlaylistStore` (currently lines 232-245), each with the doc comment from design §3.3. |

**Verify:**
```bash
go build ./... 2>&1 | head -40          # EXPECT: failures in PebbleStore + mocks only
go vet ./internal/database/...
```
Green means: the only build errors are "PebbleStore does not implement
UserPlaylistStore" and the two mock types. Anything else means the type
placement is wrong.

---

## Step 2 — PebbleStore implementation of the entry keyspace

| File | Intent |
|---|---|
| `internal/database/pebble_store_playlist_entries.go` | **create.** All six methods. Keys exactly as design §D1. `ReplacePlaylistEntries` = one `p.db.NewBatch()`: delete the old `uplent:<id>:` range + old `idx:uplent:bf:*` + `idx:uplent:unres:*` entries for that playlist, write the new ones, then **re-fetch the owning row via `p.GetUserPlaylist(id)`, mutate ONLY `BookIDs`/`EntryCount`/`UnresolvedCount`, marshal that, `Set("upl:"+id)`** — never marshal a caller-supplied struct. Same re-fetch discipline in `SetPlaylistImportProvenance`. |
| `internal/database/pebble_store_playlist_entries_test.go` | **create.** See tests below. |
| `internal/database/iface_assert.go` | **modify.** It already carries compile-time `var _ Store = (*PebbleStore)(nil)`-style assertions; confirm nothing new is needed, else add. |

**Tests (all in the new `_test.go`):**
- `TestPlaylistEntryKeyspace_DoesNotCollideWithUserPlaylistScan` — 🔴 the
  load-bearing one. Create a playlist, call `ReplacePlaylistEntries` with 3
  entries, then call `ListUserPlaylists("", 0, 0)` and assert it returns
  **exactly 1** row. This is the regression test for the collision analysed in
  design §D1.a (`listUserPlaylists` iterates `["upl:", "upl:~")`,
  `pebble_store_playlists.go:262-264`, and `encoding/json` would silently decode
  an entry blob into a blank `UserPlaylist`).
- `TestReplacePlaylistEntries_PreservesUnrelatedPlaylistFields` — 🔴 the
  write-back-wipe test. Create a playlist with `Name`, `Description`, `Query`,
  `SortJSON`, `Limit`, `ITunesPersistentID`, `ITunesRawCriteriaB64`,
  `CreatedByUserID`, `Dirty`. Call `ReplacePlaylistEntries`. Re-read and assert
  **every one** is unchanged and `Version` incremented by exactly 1.
- `TestReplacePlaylistEntries_ProjectsBookIDsInFirstAppearanceOrder` — entries
  `[bfA→bookX, bfB→bookY, bfC→bookX]` yield `BookIDs == ["X","Y"]`.
- `TestReplacePlaylistEntries_UnresolvedEntriesArePersistedAndCounted` —
  20 unresolved of 60 → `EntryCount==60`, `UnresolvedCount==20`,
  `len(ListPlaylistEntries())==60`. (Design D2.)
- `TestReplacePlaylistEntries_IsAtomicSwapNotAppend` — call twice with different
  lists; second call's result contains only the second list.
- `TestListPlaylistsCitingBookFile_ReverseIndex` + a delete case proving the
  reverse index is cleaned on replace.
- `TestUpdatePlaylistEntryResolution_TouchesOnlyResolutionFields` — asserts
  `Title`, `Artist`, `RawPath`, `Timing`, `FirstSeenAt` survive.

```bash
go test ./internal/database/... -race -run 'TestPlaylistEntry|TestReplacePlaylistEntries|TestListPlaylistsCiting|TestUpdatePlaylistEntryResolution'
```
Green means: all 7 pass under `-race`, and in particular the collision test
returns 1 playlist not 4.

---

## Step 3 — regenerate both mock surfaces

There are **two** and both break: the mockery-generated one and a hand-written
func-field one.

| File | Intent |
|---|---|
| `internal/database/mocks/mock_store.go` | **regenerate.** `UserPlaylistStore` is listed at `.mockery.yaml:48`; `Store` embeds it. Run `make mocks` (which runs bare `mockery`). ⚠️ Per the mockery v3.7.1-vs-v2.53.6 drift note in project memory: check `mockery --version` matches what CI uses **before** committing, and commit only the diff for the playlist methods — never an unscoped whole-file regen. |
| `internal/database/mock_store.go` | **modify by hand.** Add the six `…Func` fields next to the existing playlist block (currently `internal/database/mock_store.go:262-270`) and the six method bodies next to `CreateUserPlaylist` (`:1575`). |

**Verify:**
```bash
make mocks
make mocks-check          # EXPECT: pass — committed mocks match generation
make check-mock-fresh
go build ./... && go vet ./...
go test ./internal/database/... -race -short
```
Green means: `mocks-check` passes (this is a `make ci` gate, `Makefile:350`) and
the full package builds.

---

## Step 4 — format parsers (pure, no I/O, no store)

| File | Intent |
|---|---|
| `internal/playlist/parse/parse.go` | **create.** `Format` enum, `Detect(name string, data []byte) Format`, `ParsedEntry`, `ParsedPlaylist`. |
| `internal/playlist/parse/m3u.go` | **create.** `ParseM3U`. `#EXTINF:-1` → `DurationSec 0` (unknown). 🔴 **Does not `os.Stat`-filter** — the existing transient parser at `internal/scanner/scanner.go:1610-1614` drops any line that fails to stat, which is exactly the absent-evidence-as-negative bug (design D2). Cite that line in the doc comment. |
| `internal/playlist/parse/pls.go` | **create.** `ParsePLS`. `NumberOfEntries` advisory only. |
| `internal/playlist/parse/xspf.go` | **create.** `ParseXSPF`. `<duration>` is **milliseconds** → `/1000`. |
| `internal/playlist/parse/cue.go` | **create.** `ParseCUE`. Supersedes and extends `scanner.go:1567-1592` (which reads only `TITLE` + `FILE`): adds `PERFORMER`, `TRACK nn AUDIO`, `INDEX 00`/`INDEX 01` at `mm:ss:ff` with ff/75.0, and per-track `TITLE`. N tracks over one `FILE` → N entries sharing `RawPath`, distinct `Timing`. |
| `internal/playlist/parse/testdata/` | **create.** One golden file per format plus the pathological cases below. |
| `internal/playlist/parse/*_test.go` | **create.** |

**Tests:**
- `TestParseM3U_ExtinfMinusOneMeansUnknownNotZeroLength`
- `TestParseM3U_KeepsEntriesWhosePathsDoNotExist` — 🔴 D2 regression.
- `TestParseM3U_RelativeAndAbsoluteAndFileURI`
- `TestParseM3U_HTTPEntryIsRecordedNotFetched`
- `TestParsePLS_NumberOfEntriesDisagreesWithFileKeys` — keeps all `FileN`.
- `TestParseXSPF_DurationIsMilliseconds` — 🔴 input `<duration>3600000</duration>`
  must yield `DurationSec == 3600`. The library has a documented ms/sec incident
  class (`database.NormalizeDurationSec`, used at
  `internal/plugins/maintenance/relink_unlinked.go:378`; op
  `maintenance.purge-millisecond-durations` exists for the fallout).
- `TestParseCUE_IndexOffsetsAreFramesAt75fps` — `INDEX 01 02:30:37` → `150.4933…`.
- `TestParseCUE_MultipleTracksOneFileShareRawPath`
- `TestParseCUE_PregapOnlyTrackIsKeptWithWarning` — D2 / failure mode F14.
- `TestParseCUE_MultipleFileDirectives`
- `TestDetect_ByExtensionAndBySniff`
- Property test (`-short`-skippable, matching the repo's existing convention —
  `Makefile:171-172` notes playlist property tests are skipped in short mode):
  parse→export→parse round-trip preserves entry count and order.

```bash
go test ./internal/playlist/parse/... -race
go test ./internal/playlist/parse/... -race -run TestParseXSPF_DurationIsMilliseconds -v
```
Green means: every test passes and the XSPF one is visibly asserting 3600.

---

## Step 5 — `playlists.import-scan` op (dry-run capable, apply-gated)

| File | Intent |
|---|---|
| `internal/plugins/maintenance/playlist_import.go` | **create.** `playlistImportParams` mirroring `relinkParams` (`relink_unlinked.go:55-67`): `Apply bool`, `Limit int`, `Roots []string`, `Formats []string`. `playlistImportScanDef()` mirroring `relinkUnlinkedBooksDef()` (`relink_unlinked.go:69-87`) — `ResumeDrop`, `Cancellable: true`, `Isolate: false`, `Timeout: 120*time.Minute`, `ConcurrencyKey: "playlists.import-scan"`. Four phases per design §4.1, each a `linkintegrity.PhaseResult`. Enumerate + parse + resolve run under `registry.RunItems` with `Concurrency: runtime.NumCPU()`, `ErrMode: registry.ErrModeCollect`, results under a `sync.Mutex`; **all writes single-threaded after the pool drains** — copy the structure and the comment rationale from `relink_unlinked.go:107-156`. Fail the op when `report.UnreconciledPhases()` is non-empty, as `relink_unlinked.go:222-224` does. |
| `internal/plugins/maintenance/plugin.go` | **modify.** Add `p.playlistImportScanDef(),` to the slice that currently contains `p.relinkUnlinkedBooksDef(),` at line 83. |
| `internal/plugins/maintenance/playlist_import_test.go` | **create.** |

**Tests:**
- `TestPlaylistImport_DryRunWritesNothing` — a mock store whose every write
  method fails the test if called; run with default params; assert no error and
  a populated report.
- `TestPlaylistImport_ReconcilesExaminedEqualsActionedPlusSkippedPlusErrors` —
  asserts `PhaseResult.Reconciles()` (`internal/linkintegrity/report.go:134`)
  for every phase.
- `TestPlaylistImport_UnresolvedEntriesAreCountedNotDropped` — 60 entries, 20
  with no `book_file` row; assert 60 stored, `unresolved==20`, and the RECONCILE
  log line contains all four numbers.
- `TestPlaylistImport_ReimportSameHashIsNoop`
- `TestPlaylistImport_ReimportChangedHashReplacesInPlace`
- `TestPlaylistImport_ReimportOfUserModifiedCreatesNewPlaylist`
- `TestPlaylistImport_MissingSourceMarksNotDeletes` — F7.
- `TestPlaylistImport_NeverCallsBookOrBookFileWriters` — 🔴 D4. Use the
  hand-written `database.MockStore` (`internal/database/mock_store.go`) with
  `UpdateBookFunc`/`UpdateBookFileFunc`/`CreateBookFileFunc`/`DeleteBookFileFunc`
  set to `t.Fatal`.

```bash
go test ./internal/plugins/maintenance/... -race -run TestPlaylistImport
go test ./internal/plugins/maintenance/... -race -short
```
Green means: all eight pass, and the whole maintenance package is still green
(it holds the relink/regroup suites that must not regress).

---

## Step 6 — source-grep guard for D4

**Intent:** make "never touches book rows" mechanically enforced, not
conventional — the same class of guard as `make sdkguard` (`Makefile:248-252`).

| File | Intent |
|---|---|
| `internal/plugins/maintenance/playlist_writeguard_test.go` | **create.** Parse `playlist_import.go`, `playlist_export.go`, `playlist_resolve.go` with `go/parser` and fail if any selector expression names `UpdateBook`, `UpdateBookFile`, `CreateBookFile`, `DeleteBookFile`, `DeleteBookFilesForBook`, `MergeBooks`, or `SaveChaptersForBook`. Doc comment cites design D4 and D7 and the `relink_unlinked.go:293-297` precedent. |

```bash
go test ./internal/plugins/maintenance/... -race -run TestPlaylistOpsNeverCallBookWriters -v
```
Green means: it passes, and it demonstrably **fails** if you temporarily add a
`store.UpdateBook(...)` line (verify this by hand before committing — a guard
that cannot fail is not a guard).

---

## Step 7 — `playlists.resolve-entries` and `playlists.export` ops

| File | Intent |
|---|---|
| `internal/plugins/maintenance/playlist_resolve.go` | **create.** Params `{apply, limit}`. `ListUnresolvedPlaylistEntries(limit)` → parallel `GetBookFileByPath` → single-threaded `UpdatePlaylistEntryResolution`. Also **demotes** entries whose `BookFileID` no longer resolves (F12). `PhaseResult` reconcile + fail-on-unreconciled. |
| `internal/plugins/maintenance/playlist_export.go` | **create.** Params `{apply, playlist_ids, format, dest}`. `dest` defaults to `config.AppConfig.PlaylistDir` (`internal/config/config.go:470`). 🔴 **Rejects** `dest` when `config.UnderFrozenITunesTree(dest)` is true (`internal/config/itunes_libraries.go:98`). Emits `#EXTM3U`, `#EXTINF:<dur or -1>,<Artist> - <Title>`, path. Exports unresolved entries using `RawPath` (D2). |
| `internal/playlist/playlist.go` | **modify.** Export `GeneratePlaylistFile` (currently unexported at line 42, reachable only from `GeneratePlaylistsForSeries` which unconditionally errors at lines 34-37) **or** leave it alone and write the emitter in the op. Prefer the latter: the existing helper sorts by `Position` then `Title` (`playlist.go:44-49`), which would silently re-order a playlist whose order is the point. If left alone, bump the header version only if the file is touched at all. |
| `internal/plugins/maintenance/plugin.go` | **modify.** Register both defs. |
| `internal/plugins/maintenance/playlist_resolve_test.go`, `playlist_export_test.go` | **create.** |

**Tests:**
- `TestPlaylistResolve_PromotesOnlyWhenRowExists`
- `TestPlaylistResolve_DemotesDanglingEntryKeepsRawPath` — F12.
- `TestPlaylistResolve_DryRunWritesNothing`
- `TestPlaylistExport_RefusesFrozenITunesDestination` — 🔴 D6. Assert an
  **error**, not a warning, for `dest = "/mnt/bigdata/books/itunes/x"`.
- `TestPlaylistExport_UnknownDurationRoundTripsAsMinusOne`
- `TestPlaylistExport_UnresolvedEntriesAreExportedNotDropped`
- `TestPlaylistExport_RoundTripsThroughParseM3U` — export then `ParseM3U` yields
  the same ordered `RawPath` list.

```bash
go test ./internal/plugins/maintenance/... -race -run 'TestPlaylistResolve|TestPlaylistExport'
```

---

## Step 8 — app API: entries, timings, grouping evidence, export stream

| File | Intent |
|---|---|
| `internal/server/handlers/playlist_entries.go` | **create.** `GET /api/v1/playlists/:id/entries` (ownership-gated via the existing `ownedByCaller`, `handlers/playlists.go:105`), `GET /api/v1/playlists/:id/export` (streams `.m3u8`, no disk write), `GET /api/v1/books/:id/playlist-timings` (design §3.4), `GET /api/v1/playlists/grouping-evidence` (design §3.5, includes `under_frozen_itunes_tree` per Q4). |
| `internal/server/handlers/playlists.go` | **modify.** Extend the narrow `PlaylistStore` interface (line 63-68) with the six new methods. **Do not** change `UpdatePlaylist`/`AddBooksToPlaylist`/`RemoveBookFromPlaylist`/`ReorderPlaylist`/`GetPlaylist` write paths — they must keep round-tripping only the fields they already own (D1). Bump header from 2.1.0. |
| `internal/server/wire_library_routes.go` | **modify.** Register the four routes after the existing playlist block (lines 77-85). Read routes gate on `auth.PermLibraryView` (`internal/auth/permissions.go:23`); export gates on `PermLibraryView`; nothing here needs `PermPlaylistsCreate` (`permissions.go:56`) because nothing here mutates. |
| `internal/server/playlist_entries_handlers_test.go` | **create.** |

**Tests:**
- `TestGetPlaylistEntries_404sForAnotherUsersPlaylist` — IDOR parity with the
  existing suite (`internal/server/playlist_handlers_test.go`).
- `TestGetPlaylistEntries_IncludesUnresolvedWithState`
- `TestExportPlaylistStream_ContentTypeAndBody`
- `TestGroupingEvidence_MarksFrozenITunesSources`
- `TestBookPlaylistTimings_EmptyWhenNoCueSource`
- 🔴 `TestGetSmartPlaylist_DoesNotWipeEntries` — the highest-value regression in
  the whole plan. `GetPlaylist` writes back on **read** of a smart playlist
  (`handlers/playlists.go:218-221`). Create a smart playlist, attach entries,
  `GET` it, then assert `ListPlaylistEntries` still returns them. This test is
  what proves D1's structural claim end-to-end.

```bash
go test ./internal/server/... -race -run 'TestPlaylist|TestGroupingEvidence|TestBookPlaylistTimings'
go test ./internal/server/... -race -short
```

---

## Step 9 — INTENTIONALLY EMPTY (scan-time hook dropped). No work.

**Do not implement anything under this number.** An earlier draft of this plan
added `OnPlaylistFileSeen(path, format string)` to `scanner.ScanHooks`
(`internal/scanner/hooks.go:10-13` — currently exactly two methods,
`OnBookScanned` and `OnImportDedup`). **Design §8 non-goal 11 drops it**, for
three reasons, all verified:

1. **Zero capability gained.** `playlists.import-scan` (step 5) already walks the
   configured roots itself and finds the same files. The hook duplicates
   discovery that already exists.
2. **Compile-breaking change to an interface with live implementers** —
   `serverScanHooks` (`internal/server/server_search.go:141`) and
   `testDedupScanHooks` (`internal/scanner/save_book_to_database_test.go:163`).
   Adding a method breaks both.
3. **Wrong blast radius.** `internal/scanner` is the one package where a
   regression creates or destroys books. Touching it for a convenience is not
   worth it.

`internal/scanner` is therefore **not modified by this workstream at all**.
`findPlaylistGroupings` (`scanner.go:1635`) and its caller (`scanner.go:1833`)
keep their current transient behaviour — see design Q2, which files the possible
retirement of that path as a separate question, not as work here.

The step number is preserved (rather than renumbering 10-14) because both this
plan and the design reference "step 10", "steps 11-12" and "step 14.1/14.2/14.3"
as stable identifiers.

**Verification for this step:** the diff touches no file under
`internal/scanner/`. Assert it mechanically before opening the PR:

```bash
git diff --name-only origin/main...HEAD | grep '^internal/scanner/' && \
  echo "VIOLATION: step 9 is empty; internal/scanner must not be touched" && exit 1
```
Green means: the grep finds nothing and the command exits non-zero on the grep
(no output above).

---

## Step 10 — 🔴 GATE: capture the ABS playlist fixture

**Nothing in steps 11-12 may be written until this produces a real fixture.**

```bash
python3 scripts/abs_capture_fixtures.py --help     # confirm the invocation
# capture against the ABS oracle:
#   GET /api/libraries/<id>/playlists
#   GET /api/libraries/<id>/playlists?limit=10&page=0     ← envelope-switch probe
#   GET /api/playlists/<playlistId>                        ← if it exists
```

| File | Intent |
|---|---|
| `testdata/abs-fixtures/get_api_libraries_id_playlists.json` | **create (captured, not authored).** |
| `testdata/abs-fixtures/get_api_libraries_id_playlists_paginated.json` | **create if the envelope differs** — `/authors` provably switches envelope on `limit`/`page` and "the two shapes share no keys" (`internal/server/handlers/abs/dto_library.go`, `authorsPageResponse` comment, verified against the oracle 2026-08-02). Assume nothing about `/playlists`. |
| `docs/plans/2026-08-05-playlists-plan.md` | **modify.** Record the captured shape and strike design §6.2's five unknowns one by one. |

**Exit criterion:** a fixture file exists in `testdata/abs-fixtures/` whose
`response.body` came from a real ABS server. If the oracle is unreachable, **stop
here and ship steps 1-8**; leaving `EmptyPage` wired is the correct behaviour,
because a guessed DTO produces a route that "exists and looks implemented while
behaving broken" — the exact failure `absRouteList()`
(`internal/server/wire_abs_routes.go:369`) exists to prevent, stated verbatim in
its doc comment at `wire_abs_routes.go:366-368`.

---

## Step 11 — ABS playlists DTO + handler (blocked on step 10)

| File | Intent |
|---|---|
| `internal/server/handlers/abs/dto_playlist.go` | **create.** DTO written **against the captured fixture**, not from recall. |
| `internal/server/handlers/abs/browse.go` | **modify.** Replace the `EmptyPage` wiring for playlists with a real `Playlists` handler. Keep `EmptyPage` for `/collections` (`handler.go:386`). Preserve `knownLibrary(c)` gating (`browse.go:333`). Project `UserPlaylist.BookIDs` — ABS is book-level (design §6.3). |
| `internal/server/handlers/abs/handler.go` | **modify.** Line 387: `r.GET("/api/libraries/:libraryId/playlists", auth, h.Playlists)`. |
| `internal/server/wire_abs_routes.go` | **modify.** No new path needed in `absReservedPathPrefixes` — `"/api/libraries/"` at line 64 already covers it. If step 10 revealed a top-level `/api/playlists/...` surface, that **does** need a new reserved prefix, or it 301s into `/api/v1` and silently breaks. Update `absRouteList()` (line 377+) either way, or `TestABSReservedPath_CoversEVERYRegisteredUnversionedRoute` never checks it. |
| `internal/server/handlers/abs/playlists_test.go` | **create.** Includes a conformance test using `conformance.LoadFixture` (`internal/syncapi/conformance/fixture.go:35`) + `Fixture.CompareBody` (`:49`). |

```bash
go test ./internal/server/handlers/abs/... -race
go test ./internal/server/... -race -run TestABSReservedPath
```
Green means: the conformance diff against the captured fixture is **empty**, and
the reserved-path guard passes.

---

## Step 12 — frontend surfacing (minimum viable)

| File | Intent |
|---|---|
| `web/src/services/playlistApi.ts` | **modify.** Add `getEntries`, `getGroupingEvidence`, `exportPlaylist`. |
| `web/src/pages/PlaylistDetail.tsx` | **modify.** Show `entry_count` / `unresolved_count`; list entries with their state when present. An unresolved entry renders its stored `Title`/`RawPath` — never hidden (D2). |
| `web/src/pages/Playlists.tsx` | **modify.** Column for entry count + unresolved badge. |
| `web/src/pages/PlaylistDetail.test.tsx` | **create/modify.** |

```bash
cd web && npm run test -- --run playlist
```
Design §9-Q6 flags that a fuller file-level UI is unscoped; do not expand here.

---

## Step 13 — 🔴 PROD DRY-RUN ACCEPTANCE GATE

**No apply may run until every number below has been observed and recorded.**
There is no prior count of playlist files in this library, so this plan does not
invent target numbers — it specifies the quantities that must be *produced and
reconciled* before anyone presses apply.

Deploy from the **main checkout**, not a worktree (`Makefile.local`'s
`LOCAL_ROOT` is hardcoded to the main checkout; deploying from a worktree ships
main's binary — see the first-aid note's "Next" item 1), then:

```bash
# from the main checkout, after the PR merges and `git pull --ff-only`
make deploy
# then, against prod:
POST /api/v1/operations  {"type":"playlists.import-scan"}          # apply defaults to FALSE
```

**Record all of these from the RECONCILE log lines:**

| Quantity | Must satisfy |
|---|---|
| `roots_walked` | equals the number of configured library roots |
| `playlist_files_found` total, and broken out by `m3u` / `m3u8` / `pls` / `cue` / `xspf` | sums to the total |
| `files_under_frozen_itunes_tree` | reported separately (design Q4) |
| `files_parsed` / `files_with_zero_entries` / `files_parse_errors` | `found == parsed + zero_entries + parse_errors` |
| `entries_parsed` | — |
| `entries_resolved` | — |
| `entries_unresolved_no_row` | — |
| `entries_unresolved_missing` | — |
| `entries_unresolved_off_root` | — |
| **per-phase reconcile** | `examined == actioned + skipped + errors` for **every** phase; `report.UnreconciledPhases()` empty (`internal/linkintegrity/report.go:173`) |
| `playlists_would_create` / `would_replace` / `would_skip_unchanged` | sums to `files_parsed` |

**Stop conditions — do NOT apply if any of these hold:**

1. `UnreconciledPhases()` is non-empty. The op already returns an error in this
   case (`relink_unlinked.go:222-224` pattern); treat any occurrence as a bug in
   the counting, not a data problem to work around.
2. `entries_unresolved_no_row / entries_parsed > 0.10`. Relink has already been
   applied (task #1 complete, ~16,027 books relinked), so the 38.2% zero-`book_file`
   population that motivated the fragment's sequencing warning should be largely
   drained. A double-digit unresolved rate means either relink did not take or the
   path-normalisation in the resolver is wrong — investigate before writing
   anything.
3. `files_parse_errors > 0` without each one being individually explained in the
   log. A parser that fails silently on a whole format is worse than not shipping
   it.
4. `playlist_files_found == 0`. That means the walk is wrong (or the library
   genuinely has none, in which case the whole workstream needs re-justifying
   before an apply is even meaningful).

**Also run, and record, the second dry run:** re-run the identical command and
assert the numbers are **byte-identical**. A dry run that is not idempotent is
not evidence.

---

## Step 14 — apply, staged

Only after step 13's gate passes and the owner signs off with a real
`AskUserQuestion` decision (per `feedback_prod_apply_review_gate`), not a text
reply.

```bash
# 1. canary — 25 playlist files
POST /api/v1/operations {"type":"playlists.import-scan","params":{"apply":true,"limit":25}}
# verify: 25 playlists exist, entry counts match the dry run's per-file numbers,
#         zero book/book_file rows changed (compare total_file_count histogram
#         via GET /api/v1/audiobooks?limit=500 paging before and after)
# 2. full
POST /api/v1/operations {"type":"playlists.import-scan","params":{"apply":true}}
# 3. resolve pass
POST /api/v1/operations {"type":"playlists.resolve-entries","params":{"apply":true}}
```

---

## Test strategy summary — exact commands and what green means

| Command | Green means |
|---|---|
| `go test ./internal/playlist/parse/... -race` | all four format parsers correct, incl. the XSPF ms→s and CUE 75fps unit tests |
| `go test ./internal/database/... -race -run 'TestPlaylistEntry\|TestReplacePlaylistEntries'` | keyspace does not collide with the `upl:` scan; entry writes preserve every unrelated `UserPlaylist` field |
| `go test ./internal/plugins/maintenance/... -race -short` | ops reconcile, dry-run writes nothing, no book/book_file writer is reachable — **and** the pre-existing relink/regroup suites still pass |
| `go test ./internal/server/... -race -short` | handlers, IDOR gating, and `TestGetSmartPlaylist_DoesNotWipeEntries` |
| `go test ./internal/server/handlers/abs/... -race` | conformance diff vs the captured fixture is empty |
| `git diff --name-only origin/main...HEAD \| grep '^internal/scanner/'` | **no output.** Step 9 is empty; `internal/scanner` is out of scope entirely (design §8 non-goal 11) |
| `make mocks-check && make check-mock-fresh` | committed mocks match generation (a `make ci` gate, `Makefile:350`) |
| `make ci` | the whole gate: `mocks-check check-mock-fresh staticcheck sdkguard test-all-short coverage-check-short` |
| `cd web && npm run test -- --run playlist` | frontend playlist tests pass |

⚠️ **Do not run only a subset after touching a store interface.** The
store-getter migration lesson (`feedback_storegetter_migration_full_suite_test`)
is that old mocks silently vacuous-pass in unrelated packages; run
`go test ./... -short` at least once before opening the PR.

---

## Rollback

| Stage | Rollback |
|---|---|
| Any step before step 14 | Revert the PR. The `uplent:` keyspace is additive and unread by any pre-existing code path, so leftover keys are inert. No migration to undo. |
| After a canary apply (step 14.1) | The op created `UserPlaylist` rows + `uplent:` entries only. Delete them with the existing `DELETE /api/v1/playlists/:id` (`wire_library_routes.go:81`) for the ≤25 created IDs — recorded in the op's log. **Nothing else changed**: D4 is enforced by the step-6 grep guard, so no book or `book_file` row can have been touched. |
| After a full apply (step 14.2) | Same, at scale. Because every created playlist carries `SourceKind != ""` and `SourcePath != ""` (design §3.2), the set is exactly identifiable; a one-off cleanup script filters `ListUserPlaylists` on `SourceKind != ""` and `ImportedAt >= <run start>`. Write that script **before** the full apply, not after. |
| After `playlists.resolve-entries` (step 14.3) | It only writes resolution fields on entries that already exist. Re-running it is idempotent. There is nothing to roll back that a re-run does not fix. |
| Frontend | Independent PR; revert alone. |
| ABS handler | Revert to `h.EmptyPage` at `handler.go:387` — a one-line change that restores the current, known-safe stub. |

---

## Post-task hygiene (CLAUDE.md, mandatory)

- `changelog.d/` fragment (do **not** hand-edit `CHANGELOG.md`).
- `todo.d/` fragment only for **new** tasks; checking the playlists task off is a
  direct edit of `TODO.md`.
- Executive summary: this qualifies — it spans multiple files with a wide blast
  radius and closes a tracked owner item. Update the **current month's** file in
  `docs/executive-summaries/` in the **same PR**, per
  `docs/process/executive-summaries.md`.
- Remove the worktree after merge: `git worktree remove .worktrees/playlists && git worktree prune`.
