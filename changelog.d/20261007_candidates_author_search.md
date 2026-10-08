### Fixed

#### Review Candidates view: "Search again" by author or partial title lists every match

"Search again" with the title cleared and an author typed showed only the
book's cached pick: the per-book search replaced an empty title with the
book's own title, scored every other answer against it, and dropped all of
them at score 0. "Search again" now runs a browse search
(`POST /audiobooks/:id/search-metadata` with `browse: true`,
`metafetch.BrowseSearch`): it asks what was typed, keeps every answer, scores
them with the existing scorer steps, deduplicates by ASIN/ISBN, and writes no
cache row. An author search answers from the local author catalog first
(`catalog_only: true`, shown at once, marked "Catalog"), matching part of the
author's name case-insensitively and a partial title; when the catalog has
nothing for that author a live Audible author listing stands in. Submitting the
same text again re-runs the search.

### Added

#### Scheduled `catalog_harvest` task

`catalog.harvest-authors` was registered but never run, so the author catalog
was empty. The new `catalog_harvest` task enqueues it live
(`dry_run: false`) over every library author once a day
(`scheduled.catalog_harvest.interval`, default 1440 minutes, 0 turns it off;
also requires `catalog.enabled`). The op's own pacing applies: 4 workers
sharing a sub-limiter at half of Audible's 8 req/s, on top of Audible's shared
token bucket, and an author is re-listed only after 30 days.
