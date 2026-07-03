<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-02-updatebook-memdb-preserve-guard.md -->
<!-- version: 1.0.0 -->
<!-- guid: d2bd6191-8300-4366-9c1a-15f8bb516655 -->
<!-- last-edited: 2026-07-03 -->

# TASK-02 — memdb preserve guard on `UpdateBook` (mirror PERF-7 BookFile guard) (STOR-1 / QUAL-2 / CTR-1 / CTR-2)

**Priority:** P0 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-02-updatebook-memdb-preserve-guard" -b agent/cr-02-updatebook-memdb-preserve-guard origin/main
cd "$REPO/.worktrees/cr-02-updatebook-memdb-preserve-guard"
git rebase origin/main
```

## Goal

Close STOR-1 / QUAL-2 (High): a memdb-stripped `Book` read via `GetAllBooks`
and written back via `UpdateBook` silently wipes `Description`, `VersionNotes`,
and every `BookSig*` dedup-signature field. Add a preserve-on-nil guard inside
`UpdateBook` — mirroring the PERF-7 guard already shipped for `BookFile` in
`UpsertBookFile` / `BatchUpsertBookFiles` — so the fields are restored from the
existing stored row whenever the incoming pointer is `nil`. Do this with **zero
extra reads**: `UpdateBook` already fetches `oldBook` via `GetBookByID`
(Pebble-direct, unstripped) before it does anything else.

## Background (verify before editing)

- `stripBookForMemdb` (`internal/database/memdb_strip.go`) nils
  `Description`, `VersionNotes`, `BookSigV1`, `BookSigV1Mask`,
  `BookSigSegments`, `BookSigBuiltAt`, and `BookSigCoveragePct` (plus
  `Author`/`Series`, which are handled by a separate hydration path and are
  NOT in scope for this task) before inserting the projection into memdb.
- `PebbleStore.GetAllBooks` (`internal/database/pebble_store.go:1391-1393`)
  delegates to the memdb projection when `p.UseMemDB && p.mem() != nil` — this
  is the production path (`UseMemDB=true`). Callers therefore receive
  stripped `Book` values with those seven fields nil'd.
- `PebbleStore.UpdateBook` (`internal/database/pebble_store.go:2664` on)
  marshals the incoming `*Book` **verbatim** — it only special-cases `ID` and
  `CreatedAt` (copied from `oldBook`) and sets `UpdatedAt`. Every other field
  on the incoming struct — including nil'd Description/BookSig* — overwrites
  the stored row.
- `oldBook, err := p.GetBookByID(id)` is the very first statement in
  `UpdateBook` (currently line 2666) and `GetBookByID` is Pebble-direct
  (`internal/database/pebble_store.go:1691`), so `oldBook` always carries the
  full, unstripped row. This means the guard needs **no new lookup** — just
  compare `book.<Field>` to nil and fall back to `oldBook.<Field>`.
  `oldBook` is also marshalled into the `book_ver:` CoW snapshot
  (`internal/database/pebble_store.go:2688-2693`) before the overwrite, which
  is why prior wipes are recoverable from snapshot history — but new wipes
  should stop happening going forward, which is what this task fixes.
- Confirmed live callers that do `GetAllBooks` → mutate → `UpdateBook`
  round-trips and are therefore exposed today:
  `internal/reconcile/reconcile.go:1115` (`GetAllBooks`) →
  `internal/reconcile/reconcile.go:1149` (`UpdateBook`), inside
  `AssignOrphanVGs`, reachable via `POST /operations/assign-orphan-vgs`. There
  are several other `GetAllBooks`/`UpdateBook` pairs in the same file (lines
  202/571, 631/707+710, 721/777, 797/834, 862, 943) and in
  `internal/merge/service.go`, `internal/quarantine/service.go`, and
  `internal/database/migrations.go` — the guard fixes all of them at the
  single chokepoint, no per-caller changes needed.
- The exact BookFile analogue to mirror is in
  `internal/database/pebble_store.go`:
  - `UpsertBookFile` (function starts at `9941`) — preserve block currently at
    `9969-9985`, guarding `AcoustIDFingerprint`, `FingerprintFailureReason`,
    `FingerprintFailureDetail`, `FingerprintDiagnosticJSON` with
    `if len(file.AcoustIDFingerprint) == 0 { file.AcoustIDFingerprint =
    existing.AcoustIDFingerprint }` / `if file.<PtrField> == nil { file.<PtrField>
    = existing.<PtrField> }` patterns.
  - `BatchUpsertBookFiles` (function starts at `9994`) — same preserve block,
    currently at `10033-10053`, with a comment explaining the maintenance
    tag-backfill footgun this guards against — use this comment as the model
    for the new comment on the Book side.
- `Book` struct field types (`internal/database/store.go`): `Description
  *string` (line 133), `VersionNotes *string` (line 167), `BookSigV1 *string`
  (202), `BookSigSegments *int` (203), `BookSigBuiltAt *time.Time` (204),
  `BookSigV1Mask *string` (205), `BookSigCoveragePct *int` (206) — all
  pointer types, so a straightforward `if book.X == nil { book.X = oldBook.X
  }` guard works for every one of them, exactly like the BookFile guard.
- **Escape hatch already exists structurally — verify, do not add new
  plumbing.** A grep across the repo for explicit `.Description = nil` /
  `.BookSigV1 = nil` / etc. assignments on `UpdateBook`-bound structs found
  none outside `stripBookForMemdb` itself. The one real user-facing edit path,
  `internal/audiobooks/update_service.go` (`UpdateAudiobook`, around line
  120), only sets `updates.Description = &desc` when the field key is present
  in the request payload (`util.ExtractStringField(payload, "description")`);
  an explicit "clear the description" edit produces `&""` (a non-nil pointer
  to an empty string), not `nil`. Because the new guard only fires on `nil`,
  explicit user-initiated clears (pointer-to-empty-string) pass through
  untouched and are NOT blocked by this guard — nil strictly means "field not
  supplied / stripped", never "explicitly cleared". Confirm this still holds
  after your edit by re-running the grep in the test step below.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func (p \*PebbleStore) UpdateBook\b\|func (p \*PebbleStore) GetBookByID\b\|func (p \*PebbleStore) GetAllBooks\b" internal/database/pebble_store.go
  grep -n "func stripBookForMemdb" internal/database/memdb_strip.go
  grep -n "func (s \*PebbleStore) UpsertBookFile\b\|func (s \*PebbleStore) BatchUpsertBookFiles\b" internal/database/pebble_store.go
  ```
  Confirm `UpdateBook`'s current body still opens with `oldBook, err :=
  p.GetBookByID(id)` and still preserves only `book.ID` and
  `book.CreatedAt` before marshalling:
  ```bash
  sed -n '/func (p \*PebbleStore) UpdateBook/,/json.Marshal(book)/p' internal/database/pebble_store.go
  ```

## Step-by-step

1. Open `internal/database/pebble_store.go` and locate `UpdateBook` (re-verify
   with the grep above — do not assume the line number from this brief).
2. Immediately after the existing block that copies `CreatedAt` from
   `oldBook` (and before `data, err := json.Marshal(book)`), add a preserve
   block for the seven memdb-stripped fields:
   ```go
   // Preserve fields stripped by stripBookForMemdb (STOR-1). Callers that
   // sourced `book` from the memdb projection (GetAllBooks on the production
   // UseMemDB path) carry nil Description/VersionNotes/BookSig* even though
   // the stored row has real values. Restoring from oldBook — already fetched
   // above via the Pebble-direct GetBookByID — costs zero extra reads.
   // Mirrors the UpsertBookFile/BatchUpsertBookFiles fingerprint-preserve
   // guard (PERF-7) — keep both in sync.
   if book.Description == nil {
       book.Description = oldBook.Description
   }
   if book.VersionNotes == nil {
       book.VersionNotes = oldBook.VersionNotes
   }
   if book.BookSigV1 == nil {
       book.BookSigV1 = oldBook.BookSigV1
   }
   if book.BookSigV1Mask == nil {
       book.BookSigV1Mask = oldBook.BookSigV1Mask
   }
   if book.BookSigSegments == nil {
       book.BookSigSegments = oldBook.BookSigSegments
   }
   if book.BookSigBuiltAt == nil {
       book.BookSigBuiltAt = oldBook.BookSigBuiltAt
   }
   if book.BookSigCoveragePct == nil {
       book.BookSigCoveragePct = oldBook.BookSigCoveragePct
   }
   ```
3. Do NOT touch any other part of `UpdateBook` — not the CoW snapshot write,
   not the path-index/hash-index update logic, not the function signature.
   This is a purely additive guard between the existing `CreatedAt`
   preservation and the `json.Marshal(book)` call.
4. Add a new test file `internal/database/pebble_book_preserve_test.go`
   (model it directly on `internal/database/pebble_bookfile_preserve_test.go`
   — same store-setup pattern, same doc-comment style) that:
   - Creates a book with non-nil `Description`, `BookSigV1`, `BookSigV1Mask`,
     `BookSigSegments`, `BookSigBuiltAt`, `BookSigCoveragePct`, and
     `VersionNotes` set (via `CreateBook` then a direct `UpdateBook` to set
     the BookSig* fields, since `CreateBook` likely doesn't accept them —
     check `CreateBook`'s signature first).
   - Calls `UpdateBook` with an incoming `*Book` that has all seven fields
     nil (simulating a memdb-sourced round-trip) but changes an unrelated
     field (e.g. `Title`) — asserts, via a follow-up `GetBookByID` (Pebble-direct,
     reflects the real stored row), that all seven fields are still present
     and unchanged, AND that `Title` was updated.
   - Calls `UpdateBook` again with `Description` explicitly set to a pointer
     to `""` (empty string, non-nil) — asserts the stored `Description` is
     now `""`, proving explicit clears still work and are not blocked by the
     new guard.
5. Bump the file header (version bump + `last-edited` date) on every file you
   touch — `internal/database/pebble_store.go` and the new test file — per
   `.standards/instructions/file-headers.md`.
6. Re-run the escape-hatch grep from the Background section to confirm no
   caller intentionally sets any of the seven fields to `nil` on a struct
   that flows into `UpdateBook` (this would now be silently overridden by
   the preserve guard — if you find one, stop and flag it in the PR
   description instead of silently changing behavior):
   ```bash
   grep -rn "\.Description = nil\|\.VersionNotes = nil\|\.BookSigV1 = nil\|\.BookSigV1Mask = nil\|\.BookSigSegments = nil\|\.BookSigBuiltAt = nil\|\.BookSigCoveragePct = nil" internal/ --include="*.go" | grep -v _test.go | grep -v memdb_strip.go
   ```

## How to test

```bash
go build ./...
go test ./internal/database/... -run TestPebbleBookPreserve -v -count=1
go test ./internal/database/... -count=1
go test ./internal/reconcile/... -count=1
go vet ./internal/database/...
```

## Acceptance criteria

- [ ] `UpdateBook` preserves `Description`, `VersionNotes`, `BookSigV1`,
      `BookSigV1Mask`, `BookSigSegments`, `BookSigBuiltAt`, and
      `BookSigCoveragePct` from the existing stored row whenever the
      incoming `*Book` has that field `nil`.
- [ ] No extra Pebble read added — the guard reuses the `oldBook` already
      fetched by the existing `GetBookByID` call at the top of `UpdateBook`.
- [ ] An incoming `*Book` with an explicit non-nil value (including a pointer
      to an empty string, for `Description`) still overwrites the stored
      value — the guard only fires on `nil`, never on non-nil "clear" values.
- [ ] New test `internal/database/pebble_book_preserve_test.go` covers both
      the preserve-on-nil case and the explicit-clear-still-works case;
      `go test ./internal/database/...` and `go vet ./internal/database/...`
      are green.
- [ ] `go test ./internal/reconcile/...` still passes (guards against
      regressing `AssignOrphanVGs` and the other `GetAllBooks`/`UpdateBook`
      round-trips in that package).
- [ ] The escape-hatch grep in step 6 was run and its output (empty or
      otherwise) is noted in the PR description.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(database): preserve memdb-stripped Book fields in UpdateBook (STOR-1)

GetAllBooks on the production UseMemDB path returns Books with Description,
VersionNotes, and all BookSig* dedup-signature fields nil'd by
stripBookForMemdb. UpdateBook marshalled the incoming struct verbatim,
so any GetAllBooks -> mutate -> UpdateBook round trip (AssignOrphanVGs,
migrations, quarantine restore, merge) silently wiped those fields. Mirror
the PERF-7 BookFile preserve guard: restore from the already-fetched
oldBook whenever the incoming pointer is nil, at zero extra read cost.

Co-Authored-By: Claude <model> <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-02-updatebook-memdb-preserve-guard
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `UpdateBook` already contains nil-checks that restore
`Description`/`VersionNotes`/`BookSigV1`/`BookSigV1Mask`/`BookSigSegments`/
`BookSigBuiltAt`/`BookSigCoveragePct` from `oldBook` before marshalling, this
task is done — verify with:
```bash
sed -n '/func (p \*PebbleStore) UpdateBook/,/json.Marshal(book)/p' internal/database/pebble_store.go | grep -n "oldBook\."
```
and confirm all seven fields appear. If the consultancy citations have
drifted and `UpdateBook` now reads differently (e.g. it has been refactored
to take a diff/patch struct instead of a full `*Book`), this guard may
already be structurally unnecessary — say so explicitly in the PR
description rather than forcing the pattern in. Rollback = revert the
commit; the existing `CreatedAt` preservation and CoW `book_ver:` snapshot
behavior are untouched by this change and remain in effect (so any wipes
that occurred before this fix ships remain recoverable from `book_ver:`
snapshots per STOR-1's advisor note — sequence any such recovery before
STOR-2's snapshot-pruning work).
