### Added

#### `GET /api/v1/diagnostics/db-census` — per-key-family census of the main Pebble store

A new key-family registry (`internal/database/keyfamilies.go`) lists every key
prefix in the main Pebble store with a description and owning file. It is the
single source for the census and, later, the generated key-schema doc. Families
nest: a parent's figures exclude its registered children, and every key range
no family covers is reported as `(unregistered)`. The registry pre-registers
the TASK-A5 timeline indexes `opv2:open:` and `opv2:done:`, and breaks the
`_system` user's records under `pref:_system:` out by sub-prefix.

The endpoint (`PermSettingsManage`, the same as db-health) reports, for each
family, keys, deletions, entries, raw key and value bytes, disk bytes, table
count and a `method`:

- `exact`: the family's estimated size is under 1M entries, so a bounded
  keys-only pass counts its live keys, tombstones and shadowed versions exactly.
  On a 72-table test store the counts matched an sstable-level reference exactly.
- `estimated`: a large family is apportioned from `SSTables(WithProperties)`
  span bytes once the exact families' entries are taken out. It reports an
  `error_bound_keys`.
- `empty`: the family has no entries.

One bounded seek per range finds the ranges that hold anything, including
tombstone-only ranges, which it detects through the iterator's `PointCount`.
Only those ranges receive shares. As a result, a fully purged family keeps its
tombstones and disk bytes, and empty families get 0. Disk bytes are apportioned
from table sizes, so the per-family sum equals the total.

Retired books (soft-deleted or merged) and the files they still own, plus
fingerprint and transcript counts, come from memdb. When memdb is cold these
fields are null with a note, never zero, and that census is not cached.
`?deep=true` adds a keys-only `book_ver:` pass for the distribution of history
entries per book; it has a 2-minute budget and reports partial results when the
budget runs out. Results are cached for 5 minutes, in a cache bounded to 8
stores. Concurrent callers share one computation that runs detached from any
single caller, so a disconnecting client no longer fails the others.
`?fresh=true` refreshes the cache. Before this, the only full count was
db-health's `KeyCount`, which iterates all 174M production keys in about 5
minutes.
