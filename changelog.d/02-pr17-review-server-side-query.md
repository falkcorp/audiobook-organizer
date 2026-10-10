### Added

- `GET .../metadata/cache/review?view=page`: the Review > Metadata lane's whole filter chain (Title box in the Library grammar, provider, confidence, hide applied/rejected/skipped/no-match, runtime, language, transcription, multi-book, chip views) evaluated on the server over the review snapshot it already holds. One evaluation per filter gives the page, the total, the rail's chip counts and an ids-only `ids=all` list for "select all N"; the last 32 evaluations are kept per cache and library generation, so paging and select-all reuse one evaluation and any write starts a fresh one. Every response carries `generation` and `Cache-Control: no-store`. Nothing in the web app calls it yet (task 02-PR18); `view=index` is unchanged byte for byte.
- On 40,000 synthetic rows one evaluation takes 0.5 ms (chips only), 1.7 ms (chips plus an RE2 title) and 9 ms (an RE2 matching every row, then sorting all of them); a 50-row page is 31 KB and `ids=all` for 40,000 books is 1.1 MB.

### Fixed

- Search patterns are bounded in size before they run, in both the Review Title box and Library search (they share one grammar).
  - A value longer than 256 bytes is a 400 that says what to shorten.
  - So is a pattern whose compiled program exceeds 500 instructions. Before this, `/(.*){1000}/` took 1.3 s over 40,000 titles and `/(?:.?){1000}zzz/` took 23.5 s; both are now refused in under a millisecond.
  - Cleanup searches such as `/.{120}/` and `/.{100,}/` are accepted.
- Search patterns are also bounded in time. A pattern under the size limit could still run for seconds: `(?:\pL?){45}zzz` is 95 instructions and took 3.2 s over 40,000 titles.
  - Every Review query and every Library request with a regex or `*` filter now times its pattern matches against one 1 s allowance per request. A Library page and its total count share that allowance, so a request whose count would run past it is refused once, with a 400 that says the search was too slow and suggests simplifying it. It never returns a partial list or a short count.
  - A regex or `*` pattern is matched against at most the first 16 KB of a field, cut on a character boundary; a plain word still searches all of it. Before this, one 1 MB description took 5.1 s for a single match, which no time limit could interrupt; it now takes about 80 ms.
  - What the 16 KB cut means on a field longer than that (only descriptions are): a row whose only match lies past 16 KB is not found, and a negated filter such as `-description:/spoiler/` therefore keeps it. A pattern whose answer depends on how the field ends (`$`, `\z`, `\b`, or a wildcard pattern that does not end in `*`, such as `*the end`) cannot be answered from a cut field when it matches, so the search is refused with a 400 saying so instead of answering true for a field that may not end that way. Patterns that do not look at the end (`/chapter one/`, `a*`, `*a*`) are unaffected.
  - The time counted is pattern matching only. A slow disk or cold cache does not count against the search.
  - At most half the CPU cores (minimum 2) run pattern searches at once. A request that cannot get a turn within 2 s gets a 503 with `Retry-After`.
  - A background bulk metadata fetch that finds every turn taken waits 1-4 s and tries again, up to 5 times, instead of failing; "fetch candidates for this selection" answers 503 with `Retry-After` in that case rather than a 400.
  - Searches without a pattern are not affected.
- An error that names a refused search value repeats at most 64 bytes of it: a pattern (the regex engine's own message, which quoted the whole pattern, is reduced to its reason), a numeric or duration value, a range, a filter field name and the Review Title box's field name and bare text. Every kind of Library filter value is now held to the 256-byte limit, not only text, and the set-size check (1,024 bytes in total) runs before any value is parsed.
- Library `filters` are bounded as a set: at most 8 regex or `*` filters and 1,024 bytes of values in total, each refused with a 400. Review's `q` is capped at 1,024 bytes.
