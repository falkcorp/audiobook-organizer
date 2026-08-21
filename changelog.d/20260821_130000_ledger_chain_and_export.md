<!-- file: changelog.d/20260821_130000_ledger_chain_and_export.md -->
<!-- version: 1.0.0 -->
<!-- guid: e565dcdb-171f-4910-a71b-d52c90d48ecb -->
<!-- last-edited: 2026-08-21 -->

### Added

#### The provenance ledger can now prove it wasn't rewritten, and it survives its own database

Two gaps in the file provenance ledger, both surfaced by asking whether we
should move it to a purpose-built append-only database like immudb. The answer
was no — that was already evaluated and rejected, and its inability to delete
is now a hard blocker because `AdoptOrphanEvents` legitimately *moves* rows.
Both gaps close inside the store we already have.

**A hash chain per file.** Every event carries a digest of itself and of its
predecessor, so a rewritten or deleted row breaks the link and is detectable on
read. The digest is over an explicit **length-prefixed, versioned** encoding
rather than `json.Marshal`: field order is not promised forever, a JSON tag
rename would silently invalidate every historical hash, and without length
prefixes moving a character across a field boundary leaves the concatenation
identical — the chain would be forgeable.

The link follows **append order**, not event time. Those differ, and routinely:
a pre-write observation gets recorded after the fact, and an adopted orphan
keeps its original timestamp and lands in the middle of a chain. Linking by
time would fork the chain on every honest write.

**A store-wide sequence**, assigned in the same Pebble batch as the event so a
crash cannot burn a number — a burned number is indistinguishable from a
deleted row. It buys the one thing a per-file chain cannot: a **gap proves rows
were deleted wholesale**, including the deletion of an entire file's chain,
which the chain link can never notice because the evidence goes with it.

**An append-only JSONL export**, `maintenance.file-provenance-export`. The
chain proves nothing was rewritten in place; it cannot help if the database is
corrupted or restored from a backup taken before an incident — and a ledger
database would have had that same failure. A plain file on disk survives it.
The op never rewrites a byte it has written, resumes from a durable cursor, and
advances that cursor **only after the bytes are fsynced**: a failure duplicates
lines, which a reader collapses on `seq`, rather than skipping events forever
with nothing to say so.

Sequence slots whose event row has vanished are exported as explicit `missing`
markers rather than skipped, because that slot is the evidence.

#### Worth knowing

The verifier reports **three** states, not two. `unchained` means the events
predate this change — legitimate, and not an error. Conflating that with
`broken` would flag the entire existing library on the first run after deploy
and teach everyone to ignore the report.

Verification runs inside the export, including on a dry run. So the default
invocation — no `apply` — is a read-only ledger health check that writes
nothing. A verifier with no caller is decoration; this one has a job.

**What this does not do is *prevent* anything.** Any code holding the raw
database handle can still delete a row, and no in-process guard can forbid it.
What is now true is that doing so is loud on the next read, and that anything
already exported cannot be taken back.
