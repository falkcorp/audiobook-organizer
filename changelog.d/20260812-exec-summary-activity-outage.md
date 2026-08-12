### Added

#### Executive summary for the Activity-page memory outage

`docs/executive-summaries/2026-08-12-the-page-nobody-was-looking-at-executive-summary.md`
covers PR #2318 in plain language: the Activity page read the entire history into memory
on every request, ignored client disconnect, and was never limited in how many copies
could run at once — thirty were still allocating against a 30 GB cap with zero clients
connected.

Written per `docs/process/executive-summaries.md`, which this change qualifies for on two
counts: it fixes something that took production down repeatedly, and it has a wide blast
radius (an interface change that deleted the uncancellable code path outright).

States what is not claimed: the memory snapshot explains the one crash that was captured,
not all five, and log compaction still has the same read-everything shape and must not be
triggered yet.
