### Added

#### `GET /api/v1/diagnostics/db-census` — per-key-family census of the main Pebble store from sstable metadata

A new key-family registry (`internal/database/keyfamilies.go`) lists every key
prefix in the main Pebble store with a description and owning file. It is the
single source for the census and, later, the generated key-schema doc. Families
nest: a parent's figures exclude its registered children, and every key range
no family covers is reported as `(unregistered)`. The registry pre-registers
the TASK-A5 timeline indexes `opv2:open:` and `opv2:done:`.

The endpoint (`PermSettingsManage`, same as db-health) reports totals and, per
family, keys, deletions, raw key and value bytes, disk bytes and table count.
It reads `SSTables(WithProperties)` plus span bytes and `EstimateDiskUsage` for
each key range, and never iterates the store. A table that straddles several
families is apportioned by span bytes and flagged `estimated`. Keys are sstable
entries minus deletions, so overwritten versions that are not yet compacted
count once each. Unflushed memtable contents are not counted. Every response
carries `family_figures_basis`, which labels the figures as on-disk entries.
The census also reports retired books (soft-deleted or merged) and the files
they still own, plus fingerprint and transcript counts, all read from memdb.
When memdb is cold these fields are null with a note, never zero. `?deep=true`
adds a keys-only pass over `book_ver:` that gives the distribution of history
entries per book (percentiles, buckets, top 20, orphans). Results are cached for
5 minutes and shared across concurrent callers; `?fresh=true` refreshes them.
Before this, the only full count was db-health's `KeyCount`, which iterates all
174M production keys and takes about 5 minutes.
