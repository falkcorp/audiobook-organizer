<!-- file: docs/plans/2026-10-07-stale-itunes-path.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5f4fc883-f6c3-41f4-9539-c1c4ae4d3485 -->
<!-- last-edited: 2026-10-07 -->

# Plan: clear stale iTunes paths, then retire the proven library copies

Branch `fix/stale-itunes-path-rows`, worktree `aorg-stale-itunes-path`, based on
origin/main `0135c2308`. The owner approved this plan before it was written
("Clear stale path, then retire"). This file records each decision and why.

## Goal

A prod parent book has 301 files. 300 single-file "fragment" books are proven
(sha256 at plan time) to be copies of 300 of those files. They should be retired
into the parent through the Repairs lane: trial, then the owner approves, then
apply. Every step is journaled and revertible.

Two things block that today:

1. **Each fragment's book_file row carries an `itunes_path`.** No iTunes track is
   at that path. The path exists only in our DB: neither the imported library
   nor the write-back `.itl` has a track there, and the fragments carry no PIDs.
   The fragment-consolidation fixer reads the path as "this is an iTunes book"
   and holds the row.
2. **Each fragment shares a version group with a "twin".** The twin is a
   non-primary book whose only file is the real iTunes Media file under
   `books/itunes/**`. The twin holds the row again (see "Blockers" below).

## Blockers, from the code (file:line at `0135c2308`)

- **B1: the `copy:` row is held by the fragment's own row iTunes path.**
  - `fragCandidate.itunesWhy`
    (`internal/plugins/maintenance/fragment_consolidation_fixer.go:606-615`) returns
    "row iTunes path ...".
  - `splitManualCopies` (`:3141-3155`) marks each fragment hands-off.
  - None of them is owner-eligible (B2), so `keep` is empty and the whole pair
    list goes back as one manual row (`:3162-3163`).
  - The prod skip reason is the `withPID` text built at `:3478-3479`.
- **B2: no `owner:` row forms. Proven, not suspected.**
  - In `ownerEligible` (`internal/plugins/maintenance/fragment_owner_apply.go:163`),
    `lib.groupsITunesExcept({g}, {fragment})` reads the twin.
  - The twin goes through `memberITunesWhy` (`fragment_consolidation_fixer.go:5647`),
    then `itunesCopyWhy` (`duplicate_copies_fixer.go:208`), then
    `itunesguard.OwnershipWhy`. Its file is under the iTunes library, so it is an
    "iTunes copy".
  - The pair therefore stays hands-off.
  - This reason is never shown. When `keep` is empty, `splitManualCopies` returns
    `handsOff` without the `"; not owner-applicable: …"` text it computed (`:3150`).
    The owner reads only "is an iTunes book", which is how the twin cause went
    unnoticed.
  - Test `TestStaleITunesPath_E2E_TwinBlocksBeforeFix` reproduces it on a
    synthetic fixture.
- **B3: after the path is cleared, the twin still holds the row.**
  - With no row path, the fragment is no longer an iTunes book. It is kept on the
    plain `copy:` row.
  - The parent's 301 rows still carry an AO `itunes_path`, so the parent is
    iTunes-linked. `fragmentOnlyParent` picks the fragment-only retire, and
    `lib.groupsITunes(fragGroups(fragments))` (`:3557`) holds the row on the twin.
  - If the parent's paths were cleared too, `retireITunes` (`:3561`) holds it on
    the same twin.
  - At apply time the same check runs under the lock: `fragGroupsITunesNow`
    (`:7973`), or `groupITunesNow`.
- **B4: the hold is not over-caution.**
  - The fragment is the primary and the twin is the group's only other live
    member.
  - Retiring the fragment makes `retireHandOff` crown the twin.
  - `itunesguard.MayWrite` (`retire_into.go:182`) refuses that write. Without the
    refusal, a books/itunes book would become a visible 301st duplicate.
- **B5: the framework guard refuses any row that lists the twin.**
  - `repairs.GuardBooksFor` runs over every applicable row and every owner row at
    plan time (`internal/repairs/engine.go:142`), and again at apply
    (`engine.go:751`).
  - A book with a file under books/itunes is `skipped_itunes`
    (`internal/repairs/guards.go:101`).
  - Writing the twin without listing it in `BookIDs` would bypass the guard. That
    is not done.
- **B6: the `copy-unproven:` row.** The 300 twins also claim the parent's rows by
  name and size; iTunes files are never content-read. That row is guarded at
  `guards.go:101` and stays manual. No change is needed there, except that twins
  carried by an owner row are taken off it (decision D7).

## Decisions and why

- **D1: a new fixer, `stale-itunes-path`.** No existing fixer clears an
  `itunes_path`. These were searched: `ClearITunesPID` (store, clears the PID
  too, not journaled), `recompute-itunes-paths` (writes computed paths and never
  clears), `itunes path_reconcile` / `path_repair` (only PID rows), and the
  repoint fixers. None clears a path that no track backs.
- **D2: a row is applicable only when ALL of these hold.**
  - **(a) No track backs the path.** The path's location matches no track in the
    library the app imports from (`itunes.library_read_path`, .xml or .itl) AND
    none in the write-back `.itl` (`itunes.library_write_path`).
    - Matching compares normalised locations: strip `file://localhost/` /
      `file:///`, percent-decode (`+` stays `+`), `\`→`/`, lower-case.
    - For an .itl, both the 0x0D Location and the 0x0B LocalURL of every track
      count. On the write-back library they differ for 300 of the Shadow's Edge
      tracks.
    - WHY match on both: over-matching only leaves a row unapplied. Under-matching
      would clear a path a real track uses.
    - WHY not `itunes.NewLocationPair`: it rejects every `.itunes-writeback/`
      location, which would silently drop about 46k write-back tracks.
  - **(b) No iTunes id on the book.** No PID on the book or any of its rows, and
    no `itunes` external id. Tombstoned ids count too: fail closed.
  - **(c) Both libraries read and parsed fully.** FAIL CLOSED: an empty config
    path, an open or parse error, or zero tracks marks every row
    `skipped_itunes_library_unreadable`. Nothing is applicable.
- **D3: what apply clears.** Only the stale `itunes_path` field: on the book
  (`books.itunes_path`) and/or on each of its book_file rows, wherever a stale
  value lives.
  - Each clear is journaled BEFORE the write as a new change type,
    `itunes_path_clear` (FieldName `book_file:<id>` or `itunes_path`). The write
    is then a compare-and-set against the planned value.
  - The op revert puts the value back while the field is still empty, and reports
    "already restored" when it holds the old value.
  - Nothing else is touched:
    - no book_file row is deleted;
    - no file on disk is touched;
    - nothing in iTunes is written;
    - no `internal/itunes` write code is changed (another agent works there). The
      fixer only calls the read-only parsers `itunes.ParseITL` and
      `itunes.ParseLibrary`.
- **D4: the fingerprint and the parse.** The fingerprint is each stale field's
  current value plus the verdict. A change since the plan is refused with
  `changed_since_plan`.
  - The libraries are parsed ONCE per plan, and once per apply run through
    `repairs.ApplyScoped` (`BeginApply`). Never once per row.
  - The plan loop is a `registry.RunItems` pool with `Concurrency` set explicitly
    (CLAUDE.md: the field defaults to 1).
- **D5: classes, so every count clicks through.**
  - `stale`: applicable.
  - `backed`: a track is there.
  - `has-itunes-id`: held.
  - `library-unreadable`: held.
  - Params: `book_ids` filters the trial.
- **D6: the Shadow's Edge trial is filtered to the 300 FRAGMENT ids only, not the
  parent.**
  - The parent's 301 rows are stale too: their AO URLs back no track. Clearing
    them would switch the retire from fragment-only (the parent is never written)
    to a full retire: listening state follows onto the parent and its group is
    re-ranked.
  - That is a different change, which the owner did not ask for.
- **D7: twins ride with their fragment on the parent's owner row. This is the same
  problem: retiring proven copies.**
  - A fragment whose version group holds an iTunes twin goes on
    `owner:<parent>`, with its twin(s). The owner's grant retires the twin first,
    then the fragment, into the parent, writing database rows only.
  - **WHY the twin first:** it is non-primary, so its retire demotes nobody and
    crowns nobody. The fragment's retire then finds no live member to crown. The
    hand-off writes nothing, and the iTunes guard (B4) is never asked to write an
    iTunes book.
  - **WHY on the owner row:** the twin's file is under books/itunes. The owner
    relaxed iTunes DB writes on 2026-10-01 provided files stay intact, and
    reserved such applies for himself (decision 2026-10-06, "list; I apply them").
  - **A twin qualifies only when ALL of these hold.** Otherwise the fragment stays
    where it was, held with its reason.
    - The group is exactly {fragment} ∪ twins, every twin non-primary
      (explicit false) and single-row.
    - Every twin is an iTunes copy ONLY by its file being under the iTunes
      library: no PID on the book or row, no itunes external id (tombstoned
      included), no row `itunes_path`.
    - The twin's recorded hash (book `file_hash`, `original_file_hash` or row
      hash) equals the fragment's plan-time content digest. That digest equals the
      parent row's file digest. On prod this holds 300/300.
    - The twin's file is on disk (stat only; it is never read) at the proof's
      size.
    - The twin has no external id and no listening state to carry.
    - The twin passes every other guard (Doctor Who / Big Finish / Torchwood).
  - The twins are taken off the parent's `copy-unproven:` row, so each book is
    listed and counted once.
- **D8: one narrow framework exception, not a fixer-wide opt-out.**
  - New field `repairs.Row.OwnerITunesDatabaseOnly []string`. These are books of
    an OWNER row whose books/itunes path check is lifted.
  - The exception applies only:
    - at plan, while the row is owner-applicable;
    - at apply, only under the owner's consumed grant for that row.
  - Every other guard still runs for those books: franchise by path, title,
    credits, transcription and tags; unreadable paths fail closed.
  - An applicable row that lists such books is still guarded in full. So is a
    plain or bulk apply of the owner row.
  - A re-plan whose list differs from the plan's is `changed_since_plan`.
  - WHY not `ITunesDatabaseOnly` on the 8k-line fragment fixer: that would lift
    the guard for every row it writes.
- **D9: B2's lost reason is reported.** When every pair of a copy row is
  hands-off, each pair's "not owner-applicable: …" reason is now appended to the
  row's skip reason. WHY: the B2 cause was invisible for a whole investigation.
- **Re-stamp risk (recorded, not changed).**
  - `recompute-itunes-paths` (manual, dry-run by default) and organize/rename set
    `itunes_path = ComputeITunesPath(file_path)` on every row they touch.
  - A cleared row can therefore get its computed path back if one of them runs
    before the consolidation re-plan.
  - Run the consolidation plan and owner apply straight after the stale-path apply.

## Files

- `internal/plugins/maintenance/itunes_stale_path_fixer.go`: new fixer.
- `internal/plugins/maintenance/itunes_stale_path_fixer_test.go`: new tests.
- `internal/plugins/maintenance/plugin.go`: registration.
- `internal/repairs/writer_files.go`: `ClearBookFileITunesPath`, `ClearBookITunesPath`.
- `internal/undo/restorable.go`, `internal/undo/engine.go`, `internal/undo/current.go`:
  the change type, classifier and preflight.
- `internal/audiobooks/revert.go`: the revert case.
- `internal/repairs/fixer.go`, `internal/repairs/guards.go`, `internal/repairs/engine.go`:
  D8.
- `internal/plugins/maintenance/fragment_owner_apply.go`,
  `internal/plugins/maintenance/fragment_consolidation_fixer.go`: D7 and D9.
- `changelog.d/` fragment.

## Steps

1. Stale-path fixer, journal type, revert and preflight, plus tests. Commit.
2. Twins on the owner row (D7, D8, D9), plus tests including the end-to-end test.
   Commit.
3. `go build ./...`, `go vet`, and `go test -race -short` on the touched packages
   (maintenance with `-timeout 60m`).

## Tests (synthetic fixtures only)

- A stale path is cleared, and the revert restores it.
- A real track at the path (imported library or write-back `.itl`) is NOT
  applicable.
- A PID on the book or a row, or an itunes external id, is NOT applicable.
- An unreadable or unparsable library means nothing is applicable.
- `changed_since_plan`: the path changed after the plan.
- The framework exception:
  - an iTunes-tree book NOT on the list still skips;
  - an applicable row with the list still skips;
  - a plain apply of the owner row refuses;
  - Doctor Who still refuses.
- The twin rules each refuse when broken: twin PID, primary twin, twin listening
  state, hash mismatch, third group member.
- End to end, shaped like prod:
  - Setup: a fragment with a stale path, its twin under books/itunes, and an
    iTunes-linked parent whose rows' paths back no track.
  - Before the fix the row is held.
  - Stale-path apply.
  - Fragment-consolidation re-plan: the owner row lists fragment and twin.
  - Owner apply retires both into the parent, writing nothing on the parent.
  - The op reverts restore everything.

## Rollback

- Every write is in the apply op's journal: `POST /operations/<apply op>/revert`
  restores the paths (stale-path apply) and un-retires the books (consolidation
  apply).
- The code change itself reverts with `git revert`. No schema or index changes.
