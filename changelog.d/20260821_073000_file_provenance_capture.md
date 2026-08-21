<!-- file: changelog.d/20260821_073000_file_provenance_capture.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5ad0fbc3-e60e-401a-946d-ac92f0617705 -->
<!-- last-edited: 2026-08-21 -->

### Added

#### Capture a file's hash before anything touches it

`maintenance.file-provenance-capture` walks directories outside the library,
digests each audio file, and records a provenance event.

The library's own files get an event whenever the organizer writes tags. Files
outside it have never been hashed by anything, so the first time we copy, move,
or retag one, the pristine state is already gone — there is no earlier value to
compare against, which is precisely how a tag-stripping incident became
unrecoverable. `/mnt/bigdata/books/abooks` is the concrete case: 5,192 files,
6 of them hashed.

These events are orphans — there is no `book_file` row yet — keyed by
full-file SHA and adopted into a real chain on import, so a file observed
outside the library and later imported reads back as one continuous history.

Four behaviours are deliberate and each is pinned by a test:

- **Roots are required, with no default.** A default would mean a mistyped or
  omitted parameter silently starts a full-file SHA-256 over every mounted
  volume.
- **A dry run does not hash.** Hashing is the entire cost of this op, so a dry
  run as expensive as the real thing is one nobody would run first. It reports
  what it would capture.
- **Re-running is idempotent.** An observation already in the ledger for the
  same content at the same path is skipped, so repeated sweeps do not pile
  duplicates into an append-only store. A file whose content *changed* is a
  genuinely new observation and is recorded.
- **What the cap left out is reported, and one bad directory does not end the
  sweep.** An unreadable subdirectory is counted and walking continues past it;
  a sweep that quietly stops early is the false confidence this op exists to
  remove.
