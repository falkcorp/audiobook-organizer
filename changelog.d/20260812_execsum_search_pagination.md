### Added

#### Executive summary for the search pagination and count fix

`docs/executive-summaries/2026-08-12-the-second-page-that-was-never-there-executive-summary.md`
covers PR #2326 in plain language: search returned an empty page two for every filtered
query, and the reported result count was the length of the page rather than the number of
matches.

Written per `docs/process/executive-summaries.md`, which this change qualifies for on blast
radius — the library screen always sends a filter, so this was every user-facing search
rather than an edge case.

Includes the before/after measurement taken on the live server (1/0/0 becoming 5/1/0 across
the first three pages, count constant at 6 across every page size), and states what is not
fixed: multi-word queries still behave close to an OR, and counts past 10,000 matches are a
logged lower bound rather than exact.
