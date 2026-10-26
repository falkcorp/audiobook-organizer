Made the callers of the retired-version read filters retired-aware, the third
of five stacked changes for retired book versions, and added the locking tests.
A retired row is a version a live book keeps out of its own file list, so code
that treats a book file as "owned" or "a candidate" now tells the two apart.
Dedup (the exact AcoustID, LSH and scan collectors), the iTunes heal, the
Deluge link backfill and the regroup snapshot skip retired rows. The iTunes
importer treats a track whose PID only a retired row holds as already in the
library: it is skipped, no second book is created, and nothing is written onto
the holder. The iTunes and external-ID backfills map a retired row's PID to its
holder, a Deluge torrent already held by a retired version is refused with its
own reason, and the version-group cleanup keeps a duplicate that holds only
retired rows instead of deleting its file and then failing. The scanner treats
a file whose only claims are retired rows, and scanned content that matches a
retired version by hash or segment vote, as known retired content and skips the
import rather than rescanning or growing the holder; a hash check that cannot
be answered completely skips the import and retries on the next scan. The
path-claim checks (regroup, junk-title, consolidation-leftovers, merge, version
groups, Deluge import, repoint journaling) read the unfiltered path getter so a
retired row counts as occupying its path. The signal coverage reports leave
retired rows out of their population, and the database census reports them as
`retired_version_files`. `ListBookFiles` takes `include_retired`, and the admin
debug view lists retired rows flagged. The include-retired helpers resolve the
retired-row reader and fail closed for a store that does not offer it. Nothing
retires a row yet, so running behavior does not change.
