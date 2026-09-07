### Changed

#### Activity SQLite `details` column is now zstd-compressed (pure-Go)

The SQLite activity backend stored the `details` payload as raw JSON. A class of
`change` rows carries multi-MB blobs (e.g. iTunes `ApplyITLOperations` dumps);
Pebble stores the activity keyspace block-compressed, but SQLite has no
transparent text compression, so those blobs landed raw and ballooned
`activity.sqlite` past 30 GB on prod, forcing the SQLite re-enable to be rolled
back (2026-09-07).

`details` is now compressed at the application layer with pure-Go zstd
(`klauspost/compress`, no cgo — matching the `modernc.org/sqlite` driver), stored
as a tagged BLOB: a 1-byte format tag (`0x00` raw, `0x01` zstd) prefixes the
value. Payloads under a threshold stay raw so tiny rows never grow, and a payload
that fails to shrink is kept raw too, so the stored size never exceeds the
original plus one byte. A legacy untagged raw-JSON value (leading `{`/`[`) still
reads back, so a pre-change database loads unchanged. Only `details` is
compressed — every other column is queried, indexed, or searched and is tiny.
This restores a Pebble-comparable footprint and unblocks re-enabling the SQLite
activity backend (still off on prod pending that separate step).
