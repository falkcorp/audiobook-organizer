<!-- file: changelog.d/20260820_144500_metadata_review_counts.md -->
<!-- version: 1.0.0 -->
<!-- guid: 94f6be6a-306c-4934-b07a-87cb32311485 -->
<!-- last-edited: 2026-08-20 -->

### Fixed

#### The review summary counted books it could not show you

The metadata review rail reported "10,730 matched" and "14,306 total" over a list that
could never hold more than 5,774 rows. The counts were tallied over every cache entry
whose book still existed, while the list additionally dropped any entry with no stored
candidate — and the `errors` field was hardcoded to zero, so nothing in the response
hinted that roughly 5,178 rows had gone missing between the count and the list.

The endpoint now decides what is reviewable first and derives the counts, the page and
the total from that one set, so they cannot disagree. `errors` reports real decode
failures. What the cache holds but nobody can act on — a missing book, or an entry with
no candidate ever stored — is reported separately as `unreviewable` and shown as its own
chip, because the honest answer to "why is this number smaller than I expected" is a
number, not a subtraction the reviewer has to work out.
