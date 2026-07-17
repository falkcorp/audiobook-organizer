<!-- file: docs/agent-tasks/metadata-matching/TASK-06-author-series-history.md -->
<!-- version: 1.0.0 -->
<!-- guid: 01df72b2-9da7-4546-8ffd-8929ce8df3ab -->
<!-- last-edited: 2026-07-10 -->

# TASK-06 — Author/series ID resolution + legacy history-stub retirement (INIT-3-T4) ⚠ review-critical

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero behavior change until an operator tunes them.
**File-ownership:** none within this workstream (`internal/metadata/enhanced.go` + the `internal/server/handlers/metadata/` call-site files are touched by no sibling task). Writing to a prod bulk-update path (creates author/series rows, writes book IDs) makes this ⚠ review-critical.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class ⚠ coordinator line-review · prod-write-path subagent · **Why:** starts writing `book.AuthorID`/`SeriesID` and creating author/series rows on a production path (roll-forward data) + memdb-slim write-back footgun · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-matching-author-series-history" -b agent/metadata-matching-author-series-history origin/main
cd "$REPO/.worktrees/metadata-matching-author-series-history"
git rebase origin/main
```

## Goal

Implement the two author/series TODOs in `internal/metadata/enhanced.go` — resolve author NAME →
`book.AuthorID` and series NAME → `book.SeriesID` in the bulk-update path using the EXISTING
lookup/create store methods — and retire the DEAD legacy metadata-history stub in the same file.
REUSE `GetAuthorByName`, `CreateAuthor`, `GetSeriesByName`, `CreateSeries`, and the EXISTING
`RecordMetadataChange` change-history store — do NOT write new name-lookup logic, a parallel
author/series creation path, or ANY new history storage.

**DESCOPED BY REVIEW — do not build a history store.** The original T4 wording ("metadata-history
never implemented") described only a dead stub. A complete metadata-change-history subsystem
already ships end-to-end: `MetadataChangeRecord` (`internal/database/store.go:872`), PebbleStore
impl (`internal/database/pebble_store_metadata.go:68,85,117`), hand-written MockStore twins
(`mock_store.go:124-126,478-492`), writes from every apply/writeback path
(`internal/metafetch/service_writeback.go:905`, `service_apply.go:188`, `service.go:291`), live
routes (`/audiobooks/:id/metadata-history[/:field]` + undo, `wire_audiobooks_routes.go:55-57`),
and a shipping frontend (`web/src/components/MetadataHistory.tsx` →
`bookdetail/BookDetailDialogs.tsx:491`). Line numbers here are as of 2026-07-10 — every one of
these citations has a re-verify grep in the descoped-citation block under "Re-verify these
anchors" below; trust the greps, not the numbers. Adding `MetadataHistoryEntry` /
`SaveMetadataHistory` / a new keyspace / mock twins / mockery regen would rebuild a wired
subsystem — spec Decision 6 forbids it. There is NO store-interface change and NO mock/mockery
work in this task.

## Background (verify before editing)

- The TODOs: `// TODO: Resolve author name to ID...` (~254), `// TODO: Resolve series name to
  ID...` (~256) inside `BatchUpdateMetadata`'s per-item body.
- The dead stub to retire (~651-668): the `MetadataHistory` type (~64, anchor grep below), the `RecordMetadataChange`
  FREE FUNCTION (~653 — not the store method of the same name), and `GetMetadataHistory` (~666,
  returns "not yet implemented"). Grep-verified: nothing outside `enhanced.go` + `enhanced_test.go`
  references them. DELETE all three (and their tests); only if your own grep finds a hidden
  consumer, delegate `GetMetadataHistory` to the existing `store.GetBookChangeHistory` instead.
- Existing helpers to REUSE: `GetAuthorByName` (`internal/database/pebble_store_authors.go`, ~93),
  `CreateAuthor` (~113), `GetSeriesByName(name string, authorID *int)`
  (`internal/database/pebble_store_series.go`, ~91), `CreateSeries(name string, authorID *int)`
  (~116), and the store method `RecordMetadataChange(record *MetadataChangeRecord)`
  (`internal/database/iface_misc.go:268`) for recording ID changes.
- **Interface widening:** `BatchUpdateMetadata(updates, store database.BookStore, validate)` —
  `BookStore` (`type BookStore interface`, `internal/database/iface_book.go`, anchor grep below)
  has NO author/series/history methods. Widen the parameter: either a small local composed
  interface (`BookStore` + the needed `AuthorStore`/`SeriesStore` + `RecordMetadataChange`
  methods) or `database.Store`. Update the one production call site
  (`internal/server/handlers/metadata/handler.go` — grep `BatchUpdateMetadata` below). The type
  the handler passes is its own `MetadataStore` interface (`type MetadataStore interface`,
  `internal/server/handlers/metadata/interfaces.go`, anchor grep below): its doc comment explains
  it embeds `database.BookStore` precisely so it can be passed straight to
  `metadata.BatchUpdateMetadata` without a cast, and it ALREADY declares
  `GetAuthorByName`/`CreateAuthor` — after widening `BatchUpdateMetadata`'s parameter, extend
  `MetadataStore` with whichever series/history methods your new parameter type requires that it
  does not yet declare. This is a signature widening only — no `internal/database` interface
  itself changes.
- **memdb-slim write-back footgun (MANDATORY):** the bulk-update path calls
  `store.UpdateBook(update.BookID, book)`. Before ANY `UpdateBook`, the `book` value must be the
  FULL hydrated row (fetched via the store's by-ID getter in the same function), never a slim/list
  struct — heavy fields get wiped otherwise. Verify what the surrounding code fetches
  (`GetBookByID` at ~242 today); if it is a slim getter, re-fetch by ID before mutating.
- **Edge semantics (state each in code comments AND tests):**
  - EMPTY author/series name in the update map → "no change": skip, do not clear the ID.
  - Lookup MISS → CREATE then assign.
  - Store ERROR from any of the four lookup/create calls → **fail-open on ID resolution**
    (reviewed): log the error, leave the ID unset, and STILL persist the other applied fields —
    a store hiccup never aborts the whole metadata apply. The apply itself remains fail-closed on
    `UpdateBook` errors exactly as today.
  - Every SUCCESSFUL ID change records a `MetadataChangeRecord` (field `author_id`/`series_id`,
    old → new value) via the existing store method, so a mis-resolution (name collision → wrong
    existing author) is auditable and reversible.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'TODO' internal/metadata/enhanced.go
  # expect: ~254 (author), ~256 (series); ~667 (history — the stub being retired)
  grep -n 'func (p \*PebbleStore) GetAuthorByName\|func (p \*PebbleStore) CreateAuthor' internal/database/pebble_store_authors.go
  grep -n 'func (p \*PebbleStore) GetSeriesByName\|func (p \*PebbleStore) CreateSeries' internal/database/pebble_store_series.go
  grep -n 'RecordMetadataChange(record \*MetadataChangeRecord)' internal/database/iface_misc.go
  grep -n 'func GetMetadataHistory\|func RecordMetadataChange' internal/metadata/enhanced.go   # the dead stub
  grep -rn 'metadata\.GetMetadataHistory\|metadata\.RecordMetadataChange' internal/ cmd/ --include='*.go' | grep -v internal/metadata/
  # ^ expect 0 hits (stub is dead); ANY hit = delegate instead of delete and say so in the PR
  grep -n 'UpdateBook' internal/metadata/enhanced.go   # the write-back site(s) to hydrate before
  grep -n 'BatchUpdateMetadata' internal/server/handlers/metadata/handler.go   # the call site to widen
  grep -n 'type MetadataHistory struct' internal/metadata/enhanced.go          # the stub TYPE being deleted (~64)
  grep -n 'type BookStore interface' internal/database/iface_book.go           # current param type (~147), read-only context
  grep -n 'type MetadataStore interface' internal/server/handlers/metadata/interfaces.go   # the type the handler passes (~55)
  ```
  Zero hits on the helper/anchor greps = STOP and report drift.

  Re-verify the DESCOPED-BY-REVIEW citations too (read-only context — these prove the shipped
  history subsystem exists and are never edited by this task; zero hits on any = STOP and report
  drift, since the descope rationale would then be stale):
  ```bash
  grep -n 'type MetadataChangeRecord struct' internal/database/store.go   # ~872
  grep -n 'func (p \*PebbleStore) RecordMetadataChange\|func (p \*PebbleStore) GetMetadataChangeHistory\|func (p \*PebbleStore) GetBookChangeHistory' internal/database/pebble_store_metadata.go   # ~68/85/117
  grep -n 'RecordMetadataChangeFunc\|func (m \*MockStore) RecordMetadataChange' internal/database/mock_store.go   # hand-written mock twins
  grep -n 'RecordMetadataChange' internal/metafetch/service_writeback.go internal/metafetch/service_apply.go internal/metafetch/service.go   # apply/writeback writers
  grep -n 'metadata-history' internal/server/wire_audiobooks_routes.go   # live routes (~55-57)
  grep -n 'MetadataHistory' web/src/components/bookdetail/BookDetailDialogs.tsx   # shipping frontend (~491)
  ```

## Step-by-step

1. In `enhanced.go`'s bulk-update path, replace the two TODOs: if `update.Updates["author"]`
   (string, non-empty) — `GetAuthorByName`; on miss `CreateAuthor`; set `book.AuthorID`. Same for
   series via `GetSeriesByName(name, book.AuthorID)` / `CreateSeries`. Apply the edge semantics
   from Background verbatim (empty → skip; miss → create; store error → fail-open, log + leave ID
   unset + continue). Confirm the `book` being written back is a full by-ID hydrated row
   (memdb-slim footgun above); re-fetch if not.
2. Record each successful ID change as a `MetadataChangeRecord` (old → new) via the existing
   `RecordMetadataChange` store method, using the "who/when" values available in that scope. A
   recording error is logged, never fatal (same fail-open posture).
3. Widen `BatchUpdateMetadata`'s store parameter (Background: composed local interface or
   `database.Store`) and update the handler call site + its `interfaces.go` type. No changes to
   any `internal/database` interface, mock, or generated file.
4. Retire the dead stub: delete the `MetadataHistory` type, the `RecordMetadataChange` free
   function, and `GetMetadataHistory` from `enhanced.go` (plus their tests) — or, if the
   dead-code grep in Background found a consumer, delegate `GetMetadataHistory` to
   `store.GetBookChangeHistory` and say which caller forced delegation in the PR description.
5. Tests in `internal/metadata/enhanced_test.go` (use the existing `MockStore` Func-fields —
   no new mocks): `TestAuthorSeriesResolution` — existing name reused (no duplicate created);
   unknown name created ONCE; empty name skipped without clearing; store-error → other fields
   still applied, ID left unset (fail-open); ID change recorded as `MetadataChangeRecord`.
   `TestLegacyHistoryStubRetired` — compile-level absence (or delegation) of the stub trio.
6. Purely surgical elsewhere: no signature changes to existing store methods; no edits to
   unrelated interface methods; no `internal/database` file touched.
7. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added — the empty-name
   skip preserves existing no-op behavior and is covered by the "empty name skipped without
   clearing" test).
8. Bump headers on every touched file; keep existing guids.
9. **Run the FULL suite** — prod-write-path discipline: `go test ./... -short` (never a subset;
   the widened call-site signature can break unexpected packages).

## How to test

```bash
make ci
# caveat: staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
# you changed; the merge gate is Minimal CI green.
go test ./... -short   # FULL suite — mandatory for prod-write-path changes
```

## Acceptance criteria

- [ ] `grep -n 'TODO' internal/metadata/enhanced.go` no longer shows the author/series TODOs
- [ ] `grep -n "metadata history not yet implemented" internal/metadata/enhanced.go` returns 0 hits (stub retired or delegated)
- [ ] `git diff origin/main --stat -- internal/database/` shows NO changes (no new store surface — Decision 6)
- [ ] `TestAuthorSeriesResolution` proves: reuse (no dup), create-once, empty-name skip does not clear IDs, store-error fail-open (other fields still applied), `MetadataChangeRecord` written per ID change
- [ ] `TestLegacyHistoryStubRetired` green
- [ ] FULL `go test ./... -short` green
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci`, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(metadata): author/series ID resolution + legacy history-stub retirement (INIT-3-T4)

Bulk updates now resolve author/series names to IDs via the existing
GetAuthorByName/CreateAuthor and GetSeriesByName/CreateSeries helpers
(hydrated-row write-back; fail-open on resolution store errors), recording
each ID change through the existing MetadataChangeRecord history. The dead
enhanced.go history stub (shadowing the shipped metadata-history subsystem)
is retired — no new store surface.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR — NO SELF-MERGE (⚠ review-critical)

```bash
git push -u origin agent/metadata-matching-author-series-history
gh pr create --fill
# STOP HERE. Do NOT run `gh pr merge`.
```

**This task has NO merge command in ANY run mode (reviewed — a review gate that exists in only
one run mode is not a gate).** In standalone mode: push, open the PR, post the acceptance
checklist + COMPLETED/REMAINING/BLOCKED counts, and STOP — coordinator/human line-by-line review
is a hard precondition for merge (prod-write path: creates author/series rows and writes book
IDs). Under a coordinated sweep, STOP after commit — the coordinator owns push/PR/merge and
performs the same line review before merging.

## Idempotency / Rollback

If the author/series TODOs are gone from `enhanced.go` and resolution code (GetAuthorByName →
CreateAuthor fallback) is present, this task is already applied — run the acceptance checks
instead of re-applying. **Rollback is roll-FORWARD for data (reviewed — do not overclaim):**
reverting the PR stops FUTURE author/series resolution, but does NOT undo already-written
`book.AuthorID`/`SeriesID` links or `CreateAuthor`/`CreateSeries` rows — a mis-resolution
persists through the revert. The per-change `MetadataChangeRecord` rows (old → new ID) are the
audit trail for manual or scripted reversal. The stub retirement is pure code deletion; a revert
restores it verbatim.
