### Added

#### Scans stop re-reading files that are still being written

An incremental scan re-read and re-hashed a file every single pass for as long
as that file kept changing. A book being copied onto the library, a download
still landing, a file being retagged by another tool — each one changed mtime
and size between scans, so the scanner read it again, hashed it again, and
stored whatever half-written metadata happened to exist at that moment. On a
44k-file library that is a large amount of repeated work spent on the files
least likely to yield a good answer.

Scans now wait for a file to go quiet before re-reading it. A file the library
already knows about is only re-read once its mtime is at least
`min_rescan_age_hours` old (default 144 — six days). Set it to `-1` to turn the
behaviour off entirely.

This deliberately does **not** delay discovery, and it is not a general scan
cooldown:

- a path the library has never seen is new, and is read immediately;
- a book explicitly flagged for rescan (the per-book force-rescan button) is
  read on the next scan regardless of its age;
- a full sweep (`force_update`) disables the scan cache outright, so the gate is
  never consulted at all.

The scan summary now reports how many files were held back, broken out from the
files that were skipped as genuinely unchanged — the two mean different things,
and a run dominated by held-back files means something is churning the library
rather than that the cache is doing its job.

### Fixed

#### A file discovered mid-write is no longer stuck with partial metadata

Adding the age gate on its own would have introduced a new way to lose data. A
file discovered part-way through being written is unknown to the scan cache, so
it is read immediately and a book row is created from whatever bytes existed at
that instant. When the write finished, the mtime change would have put that row
behind the age gate for a full six days, leaving the library showing metadata
read from a partial file.

The scan-cache write-back now re-arms the per-book rescan flag whenever it
stamps a file that is still inside the age window, so such a file is re-read on
the next scan instead of being deferred. Before the gate this case healed
itself on the very next pass; it still does.
