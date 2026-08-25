<!-- file: docs/diagnostics/2026-08-24-scan-adds-no-new-books-root-cause.md -->
<!-- version: 4.0.0 -->
<!-- guid: 60ab5a31-b0bf-4055-985b-a4b16604e8a6 -->
<!-- last-edited: 2026-08-24 -->

# Why your new books never appear

**The scan IS finding them and IS adding them. They are in the database right now.**
Two separate things are wrong: the rows it writes are structurally malformed (one
book per track, folder name as author, filename as title), and 536 books sit in
version groups that elect no primary, which makes them unreachable from a library
page that shows primary versions only.

Nothing was changed on the server. This is a diagnosis.

## What is actually happening

New books were ingested from `/mnt/bigdata/books/newbooks/audiobooks/` **tonight at
22:49 and 22:52**. So the scan reaches the folder and writes rows. But look at what
it wrote:

| title | author_name | is_primary_version | version_group_id |
|---|---|---|---|
| `Pratchett 080` | Terry Pratchett Carpe Jugulum | **false** | vg-1cf267c09def66e1 |
| `Pratchett 079` | Terry Pratchett Carpe Jugulum | **false** | vg-c71808ede39c6d8f |
| `Pratchett 078` | Terry Pratchett Carpe Jugulum | **false** | vg-2a6adc3783224fcf |
| `01` | The Sapphire Crescent | **false** | vg-c4f81e91c3a2fc24 |
| `03` | The Sapphire Crescent | **false** | vg-d75b31e3f31438db |

Four separate defects are visible in that one table.

### 1. Every track became its own "book"

*Carpe Jugulum* is one audiobook of ~80 mp3 files. It became **~80 separate book
records**, one per file, titled `Pratchett 001` … `Pratchett 080`. It should be one
book with 80 files attached.

### 2. The folder name became the author

The author of *Carpe Jugulum* is Terry Pratchett. The database says the author is
**"Terry Pratchett Carpe Jugulum"** — the folder name, verbatim. Likewise "The
Sapphire Crescent" is recorded as an author; it is a title.

### 3. The filename became the title

`Pratchett 080`, `01`, `03`. Those are file stems, not titles.

### 4. **This is the one that makes them invisible.** Every row is
`is_primary_version = false`, and each sits alone in its own version group.

A version group with exactly one member, where that member is marked non-primary,
has **no primary at all**. The library page requests primary-only by default. So
nothing in that group can ever be returned. The books exist and cannot be reached.

## The scale — measured, and smaller than it first looked

I first reported "16,460 books are invisible." **That was wrong**, and the correction
matters because it changes the remedy.

16,460 is the count of books with `is_primary_version = false`. Most of those are
*legitimate* secondary versions — `organized_source` rows and similar — sitting in a
group that DOES have a primary, and reachable through it. That is the design working.

The real question is how many books sit in a group with **no primary at all**. The
repair job answers it directly. `POST /api/v1/operations/elect-missing-primaries?dry_run=true`
(read-only; the handler defaults to dry-run) against prod:

| | count |
|---|---|
| total checked | 61,281 |
| groups scanned | 29,668 |
| **groups with no primary** | **312** |
| — of those, singleton groups | 261 |
| — of those, multi-member groups | 51 |
| **books trapped (genuinely unreachable)** | **536** |
| books with no version group at all | 5,840 |
| errors | 0 |

**536 books are truly unreachable, not 16,460.** Two orders of magnitude smaller.

Note separately that **5,840 books have no version group at all**. The election does
not touch those; they are a different population and may matter more than the 536.

## A second invisible population — and a second repair that was never run

The election above only repairs books that HAVE a version group. Books with no group
are counted (`BooksWithoutGroup`) and skipped.

So a book that is **ungrouped AND explicitly non-primary** is invisible to the default
library view *and* untouched by the election. Measured: sampling the non-primary set,
such books exist and are tightly clustered — **73 of them**, in offsets 11,800–12,199,
including *A Game of Thrones*. (Verified reproducible: the same offset returns the same
35 rows and the same first title across three consecutive runs, so this is a real
cluster and not unstable pagination. Nine other offsets across the range returned zero,
which is why a sparse sample missed it entirely.)

That state is incoherent on its face: a book that belongs to no version group cannot
meaningfully be the *non-primary version* of anything.

**A repair for exactly this already exists and has never been run:**
`internal/maintenance/jobs/normalize_primary_flags.go`, job id `normalize-primary-flags`
— *"Write explicit is_primary_version=true for ungrouped books whose flag is nil
(effective-true) or incoherently false."* Its line 76 case is `!*b.IsPrimaryVersion &&
!grouped`, which is precisely this population. It has a dry-run mode.

## Summary: two repairs exist, neither has ever been run

| population | count | repair | run? |
|---|---|---|---|
| books in groups with no primary | **536** (312 groups) | `elect-missing-primaries` | never |
| ungrouped but explicitly non-primary | **~73** | `normalize-primary-flags` | never |

Both are wired, both default to dry-run, and both are one authenticated POST away.
**Neither was run here — mutating prod rows is the owner's decision.**

This does not fix the malformed rows the scan writes; it makes the already-ingested
books reachable. The malformed-row defects are separate and scanner-side.

## What was NOT wrong — two dead ends, recorded so nobody re-walks them

**The scan root is fine.** An earlier version of this document claimed `root_dir`
(`/mnt/bigdata/books/audiobook-organizer`) was the wrong folder because the new books
live in a sibling directory. **That was wrong.** `/mnt/bigdata/books/newbooks` is a
configured, enabled **import path** — and import paths live in the database, not in
the config file, which is why dumping the config and finding no ingest setting proved
nothing. A default scan deliberately does not walk `root_dir` at all
(`scanner/service.go:293` records the reasoning: root_dir is organize's *destination*,
not a source). **Do not repoint `root_dir`.** It would drag `bkup`, `CK.rar`,
`booksonic` and 25 other entries into scope and would fix nothing.

**`last_scan` being empty on `newbooks` is a red herring.** The only code in the tree
that writes it is `internal/server/folder_autoscan_op.go:176`, which belongs to the
`library.folder-auto-scan` operation — a *different* operation from `library.scan`.
`library.scan` never writes it. With `auto_scan_enabled = false`, that op does not run,
so the field stays empty no matter how many successful scans happen. `book_count` comes
from the same line and is equally stale.

**The service is healthy.** Restarted 2026-08-24 17:55:20 EDT, `/api/health` 200.
An earlier claim that it had logged nothing since Aug 11 was an artifact of a
`timeout` truncating a large journal dump.

**`AUDIOBOOK_ROOT_DIR=/var/lib/audiobooks`** in the systemd unit points at a
non-existent directory, but `viper.AutomaticEnv()` maps it to key
`audiobook_root_dir`, not `root_dir`, so it is silently ignored. Config litter worth
removing; not the bug.

## A measurement warning for whoever works this next

`GET /api/v1/audiobooks?is_primary_version=banana` returns **16,460** — exactly what
`false` returns. The filter treats any non-`true` string as false. Anyone using that
parameter to measure must check a bogus value first, or they will believe a filter is
active when it is silently falling through to one branch.

## What needs to happen

1. **Elect a primary for the 312 groups that have none.** The repair op already exists
   and is wired: `POST /api/v1/operations/elect-missing-primaries?dry_run=false`
   (requires settings-manage). The dry run above is what it would do. This frees the
   536 trapped books. **Not run — a mutating prod repair is the owner's call.**
2. **Stop creating one book per track.** Multi-file books must group into one record.
3. **Stop using the folder name as the author and the filename as the title.**

1 is a data repair and is separately owned; 2 and 3 are scanner-side and are being
worked by the scanner-lane session.

## How this was established

`GET /api/v1/audiobooks?sort_by=created_at&sort_order=desc` (the 22:49/22:52 rows and
their full field dump); `GET /api/v1/audiobooks?is_primary_version={true,false,banana}`
for the counts and the instrument check; `GET /api/v1/import-paths`;
`GET /api/v1/config`; `scanner/service.go:293`; `folder_autoscan_op.go:176`.
