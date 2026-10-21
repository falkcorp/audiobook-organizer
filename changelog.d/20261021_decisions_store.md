### Added

#### Durable decision log and reject store (internal/decisions)

Added `internal/decisions`, the durable store behind the planned Universal Reject
feature (PR1 of the plan; no API or UI yet, so nothing user-visible changes).
It records every approve, reject, apply and undo as a labelled example with an
evidence snapshot, reason, actor and timestamp; keeps a current verdict per
proposal under a stable fingerprint (`SubjectKey`, `MaterialHash`); and vetoes a
rejected proposal and each rejected pair, scoped to the question family it was
rejected in, so a rescan cannot find it again until the owner un-rejects.
Lookups already honour a "never merge" family value that vetoes every family,
but nothing can write one until the override lands in a later PR. Skip,
approve, apply and undo are labels only and never veto. Reopening a proposal because its data changed
allows new suggestions but never lifts the veto on the originally rejected
data, only a human can lift or replace a human reject, and ids are canonicalised
(whitespace refused, ULIDs upper-cased) so one proposal cannot hide under two
spellings. Loading the veto set reads one point-in-time snapshot and writers
serialise per database engine (even through different wrappers of it), so concurrent records cannot be half-seen. Decisions are
never pruned: a test runs the log, activity, undo-journal, metrics, session,
journal, soft-delete and stale-candidate prune entry points, a reflection canary
fails when a new prune, sweep, purge, expiry or cleanup method appears on the
store types without coverage, and plugin sweeps carry a decision sentinel; a
backup round trip is pinned too. To make a record, its verdict and its veto rows
commit together, `RawKVStore` gains `ApplyRawBatch`, an atomic mixed set and
delete write (mocks regenerated), and a new `RawSnapshotter` interface gives a
point-in-time raw reader.
