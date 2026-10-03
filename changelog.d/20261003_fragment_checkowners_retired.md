### Fixed

- **Fragment-consolidation apply no longer refuses a row because a RETIRED book still names the fragment's path.** Retired books keep their rows by design, so after one apply every path a retired iTunes copy had owned made the next plan's rows "changed since plan" (26 of 29 rows on prod 2026-10-03, the Bible's 2,364 copies among them). The owner check now ignores soft-deleted owners.
