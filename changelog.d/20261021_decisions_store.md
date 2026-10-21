Added `internal/decisions`, the durable store behind the planned Universal Reject
feature (PR1 of the plan; no API or UI yet, so nothing user-visible changes).
It records every approve, reject, apply and undo as a labelled example with an
evidence snapshot, reason, actor and timestamp; keeps a current verdict per
proposal under a stable fingerprint (`SubjectKey`, `MaterialHash`); and vetoes a
rejected proposal and each rejected pair, scoped to the proposal's own family,
so a rescan cannot find it again until the owner un-rejects. A "never merge"
family value vetoes every family. Skip, approve, apply and undo are labels only
and never veto. Decisions are never pruned: a test runs every log, activity,
undo-journal and metrics prune entry point and a reflection canary fails when a
new one is added without coverage; a backup round trip is pinned too. To make a
record, its verdict and its veto rows commit together, `RawKVStore` gains
`ApplyRawBatch`, an atomic mixed set and delete write (mocks regenerated).
