## Every rescan reverts `library_state` organized→imported, emptying ABS (2026-09-07)

**Confirmed live on production.** Books progressively disappear from the ABS
layer (author pages, series, counts, browse) as scans run, because every rescan
resets `library_state` to `imported` and the organize run that follows cannot
undo it.

### Evidence (author `Nameless Author`, id 40260 — 9 books)

**All 9 carry `last_organized_at`.** Organize stamped every one; six were then
reverted:

```
organized (3)  last_organized_at = 2026-08-31 23:24   <- most recently organized
imported  (6)  last_organized_at = 2026-08-28 15:14 (x4), 2026-04-28 (x2)
```

State tracks **scan recency, not location** — all 9 sit inside the organized tree,
under both flat and nested path shapes, in both states. The survivors are simply
the rows a scan pass has not re-touched since their organize stamp.

The discriminating signature is `library_state='imported'` **AND**
`last_organized_at IS NOT NULL`: `applyScannerFields` overlays ~20 fields and
touches **zero** `LastOrganized*` fields, so only this revert produces it.

### Mechanism — three hops

1. `internal/scanner/scanner.go:2617-2641` — every scanned file builds a `dbBook`
   with a hardcoded `ls := "imported"`, `LibraryState: new(ls)` — unconditionally
   non-nil.
2. `internal/scanner/scanner.go:2912` — a rescan of a path-matched book runs
   `applyScannerFields`, then `UpdateBook`.
3. `internal/scanner/scanner.go:3345-3347` — a bare nil-check with **no
   `!locked[...]` guard**, unlike the ten tag-derived fields below it:
   ```go
   if scanned.LibraryState != nil { dst.LibraryState = scanned.LibraryState }
   ```

Since the scan root and the organized tree are the same directory, this hits
**every organized book on every scan**.

**The justification is factually wrong.** `internal/scanner/override_guard.go:44-49`
lists `LibraryState` under *"NOT GUARDED, deliberately"* because *"These are read
off the file itself; the scanner IS authoritative for them."* `LibraryState` is
**not** read off the file — it is a hardcoded literal at `scanner.go:2617`. Nine
of the ten fields listed genuinely are file-derived (`FileHash`, `FileSize`,
`Duration`, …); this one was swept in with them. Two writers claim ownership in
direct conflict — `internal/organizer/service.go:977-987` says organize owns it,
`override_guard.go:47` says the scanner does — and the scanner wins because it
runs repeatedly. (CLAUDE.md: *"When a comment explains why something must stay
wide, verify the claim before believing it."*)

**The loop closes** because the auto-organize that fires after a scan
(`internal/server/server.go:1230`) passes `&organizer.Request{BookIDs: ids}` with
an **empty** OperationID, and the `alreadyCorrect` path only stamps when
`operationID != ""` (`internal/organizer/service.go:1268`). A user-triggered
organize (`internal/server/library_core_ops.go:274`) does pass one and would fix
the library — until the next scan pass reaches those rows.

### Work

- [ ] **Stop the clobber.** Guard `LibraryState` in `applyScannerFields`
  (`scanner.go:3345-3347`) and correct the false rationale in
  `override_guard.go:44-49`. **Do this first** — any repair without it re-reverts
  on the next scan.
- [ ] **Close the re-stamp gate** at `server.go:1230` / `service.go:1268` so the
  post-scan auto-organize can stamp an already-correctly-placed book.
- [ ] **Backfill** the reverted rows, keyed on `library_state='imported' AND
  last_organized_at IS NOT NULL`. Only after the two fixes above.
- [ ] **Pin it with a test.** `internal/scanner/rescan_preserve_test.go` and
  `override_guard_test.go` contain **zero** `LibraryState` references — the
  clobber is unpinned in either direction.

### 🔴 DO NOT RUN `fix-library-states`

`internal/maintenance/jobs/fix_library_states.go` (id `fix-library-states`) is
named exactly like the fix for this and is **registered and reachable from the
ops UI** (`internal/server/maintenance_dispatcher.go:183`). It writes
**`"present"` / `"missing"`** (`:47-50`) — a vocabulary **nothing else in the
codebase produces or consumes**. Running it sets every book to a value that fails
the ABS filter, the dashboard's Needs-Organizing count, every filter chip and the
list warmer. **It would empty the ABS library, not repair it.** No cron entry
found; reachable, not scheduled. It does not exist for this drift — its premise
is filesystem presence.

- [ ] **Fix or unregister `fix-library-states`** before someone clicks it.

### Blast-radius notes for the two options

- **Relax the ABS filter** (`browse.go:190`) — touches ABS only; the native UI
  does not default-filter on `library_state`, which is why all 9 books are
  visible there today. Must still exclude `organized_source` iTunes-tree rows.
- **Repair the state values** — 🔴 `internal/itunes/service/importer.go:1381,1505`
  skip any book whose state is not `"imported"`, so repaired books **drop out of
  the iTunes organize pass**; note the standing hands-off rule on
  `books/itunes/**`. Also flips same-path dedup survivor election
  (`merge_same_path_dupes.go:289-290`), moves dashboard counts, changes
  bulk-organize/dup-detect eligibility (`web/src/pages/Library.tsx:1558,1582`),
  and needs a Bleve reindex (`internal/search/document.go:41`).

### Two incidental defects found en route

- [ ] **Broken undo record.** `internal/organizer/rename.go:230` assigns
  `"organized"` to the book *before* `:264-266` records
  `OldValue: stringOrDefault(book.LibraryState, "")`, so the undo entry is
  `organized → organized`. `internal/undo/engine.go:274-275` and
  `internal/audiobooks/revert.go:143` replay `OldValue`, so this undo can never
  restore the pre-rename state.
- [ ] **A false agreement claim.** `internal/server/handlers/abs/browse.go:176-178`
  asserts the ABS `library_state` filter and `pebble_store_stats.go`'s
  `organized_books` "are expected to agree and the acceptance check compares
  them." They cannot: stats uses a **path prefix**
  (`pebble_store_stats.go:337-344`), ABS uses the **state field**. All 9 books
  count as organized in stats; only 3 pass ABS. The named acceptance test
  (`abs/item_filter_test.go`) does not compare the two — its "unorganized" seed is
  outside the tree *and* `organized_source`, so the failing shape (inside
  rootDir, primary, unquarantined, `imported`) is absent from the fixture and
  could never have been caught.

`library_state` has **no enum and no type** — it is a bare `*string`
(`internal/database/store.go:239,391`) and `internal/batch/service.go:341-342`
accepts any caller-supplied string with no allowlist. Values in use: `imported`,
`organized`, `organized_source`, `suspicious`, `deleted`, `needs_review`, plus
the rogue `present`/`missing`. `imported` is not a documented state — it is the
zero value with a name, defaulted from nil at four independent sites.
