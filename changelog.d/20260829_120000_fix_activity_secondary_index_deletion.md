### Fixed

#### Activity log secondary indexes are now deleted with their entries (and existing orphans are repaired)

The activity log keeps three key families in PebbleDB: the primary row
(`act:<tier>:<nanos>:<ulid>`) plus two secondary indexes that let
`GET /api/v1/operations/:id/activity` and the per-book activity view answer
without scanning the log (`act:op:<op_id>:…` and `act:bk:<book_id>:…`).
`Record` wrote all three in one batch. **Nothing ever deleted the last two.**

Every deletion path — `Prune`, `Summarize`, `CompactByDay` and
`WipeAllActivity` — scans only the primary tier key range and deleted only
`kv.key`, so each pruned, summarized, compacted or wiped row left its two index
entries behind permanently. Two consequences, both real:

- `WipeAllActivity` did not wipe all activity. The rows a user asked to be
  destroyed left behind index keys that still carry the operation id and the
  book id in the key itself.
- The orphans accumulated without bound. Measured on production: the activity
  keyspace was ~1.342 GiB, of which `act:op:` alone was ~0.783 GiB — roughly
  60% — largely index entries whose primary row had been gone for months.

Both halves are fixed:

- **The leak is closed.** All four deletion paths now delete a row's
  `act:op:`/`act:bk:` entries in the same batch as the row. The ids come from
  the entry, which those paths already decode (they cannot group by day,
  operation or type otherwise), so a prune does not get slower; the timestamp
  and ULID come from the primary key being deleted rather than from the entry's
  round-tripped `Timestamp`, because a Pebble delete of a key that does not
  exist succeeds silently and one nanosecond of drift would have produced a fix
  that deleted nothing and reported no error. `WipeAllActivity` additionally
  removes both index prefixes wholesale, which is what makes its name true even
  for rows whose stored JSON will not decode.
- **The existing orphans have a route out.** A new repair pass finds index
  entries whose primary row no longer exists and deletes them, and it now runs
  as the last step of the nightly `maintenance.cleanup-activity-log` job, which
  reports the count it removed. It is idempotent, so a later run that finds
  nothing costs one scan and says zero. Index entries carrying a reference that
  cannot be turned back into a primary key at all are deleted too, and counted
  separately — no reader could follow them either.
