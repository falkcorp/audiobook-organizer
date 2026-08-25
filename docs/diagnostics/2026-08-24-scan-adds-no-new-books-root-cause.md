<!-- file: docs/diagnostics/2026-08-24-scan-adds-no-new-books-root-cause.md -->
<!-- version: 2.0.0 -->
<!-- guid: 60ab5a31-b0bf-4055-985b-a4b16604e8a6 -->
<!-- last-edited: 2026-08-24 -->

# Why your new books never appear

**The scan IS finding them and IS adding them. They are in the database right now.
They are invisible because every single one is marked "not the primary version",
and the library page only shows primary versions.**

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

## The scale

Measured live:

| | count |
|---|---|
| primary | 44,772 |
| **non-primary (invisible by default)** | **16,460** |
| total | 61,232 |

44,772 + 16,460 = 61,232, so that partitions the library exactly.

**16,460 books are in the database and hidden from the default view.**

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

1. **Elect a primary for every single-member version group.** A group of one whose
   only member is non-primary is unreachable by construction. This is the change that
   makes the existing 16,460 visible.
2. **Stop creating one book per track.** Multi-file books must group into one record.
3. **Stop using the folder name as the author and the filename as the title.**

1 is a data repair and is separately owned; 2 and 3 are scanner-side and are being
worked by the scanner-lane session.

## How this was established

`GET /api/v1/audiobooks?sort_by=created_at&sort_order=desc` (the 22:49/22:52 rows and
their full field dump); `GET /api/v1/audiobooks?is_primary_version={true,false,banana}`
for the counts and the instrument check; `GET /api/v1/import-paths`;
`GET /api/v1/config`; `scanner/service.go:293`; `folder_autoscan_op.go:176`.
