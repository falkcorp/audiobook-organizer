### Changed

- `/dedup`, `/dedup/labels`, `/authors/dedup` and `/books/dedup` now redirect into the Review workspace (the old `?tab=` values map to the matching lane, sub-view, layer or repair fixer; `book` and `band` are kept). The "Dedup" and "Gold Labels" sidebar entries are removed, `/review?lane=repairs&fixer=<id>` preselects a repair fixer, and the duplicate-authors announcement now links to `/review?lane=authors`.
