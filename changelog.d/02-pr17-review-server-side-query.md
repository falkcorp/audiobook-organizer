### Added

- `GET .../metadata/cache/review?view=page`: the Review > Metadata lane's whole filter chain (Title box in the Library grammar, provider, confidence, hide applied/rejected/skipped/no-match, runtime, language, transcription, multi-book, chip views) evaluated on the server over the review snapshot it already holds. One evaluation per filter gives the page, the total, the rail's chip counts and an ids-only `ids=all` list for "select all N"; the last 32 evaluations are kept per cache and library generation, so paging and select-all reuse one evaluation and any write starts a fresh one. Every response carries `generation` and `Cache-Control: no-store`. Nothing in the web app calls it yet (task 02-PR18); `view=index` is unchanged byte for byte.
- On 40,000 synthetic rows one evaluation takes 0.5 ms (chips only), 1.7 ms (chips plus an RE2 title) and 9 ms (an RE2 matching every row, then sorting all of them); a 50-row page is 31 KB and `ids=all` for 40,000 books is 1.1 MB.

### Fixed

- Search patterns are bounded before they run. A Title or Library search value longer than 256 bytes, or a pattern whose compiled program exceeds 100 instructions (for example `/(.*){1000}/`, which took 1.3 s over 40,000 titles, or `/(?:.?){1000}zzz/`, which took 23.5 s), is now a 400 that says what to simplify, returned in under a millisecond. This applies to both the Review Title box and Library search, which share one grammar. `view=page` also caps `q` at 1,024 bytes and stops any single evaluation after 1 s with a 400. It never returns a partial list.
