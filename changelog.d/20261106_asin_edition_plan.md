### Added

- Docs: `docs/plans/2026-11-06-asin-conflict-edition-asins.md`, the parked plan for ASIN conflicts. When a matched Audible record has a different ASIN from the book, the book will keep its own ASIN and record the other as an edition ASIN instead of refusing the apply. If another book already has that ASIN, the two are queued in the Dupes lane to merge as versions. Work starts after the audiocore split.
