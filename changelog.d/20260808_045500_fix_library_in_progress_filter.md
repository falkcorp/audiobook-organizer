### Fixed

#### The Library "In Progress" and "Finished" sidebar filters did nothing

Clicking **In Progress** (or **Finished**) under Library left the highlight
stuck on "All Books" and changed nothing on the page — no filter chip, no
change in results. Two independent bugs produced that single symptom.

**The highlight could never move.** `Sidebar.tsx` decided selection with
`location.pathname === (item.matchPath ?? item.path)`. `pathname` never
carries a query string, so "In Progress" — whose `path` is
`/library?search=read_status:in_progress` — could not match, while "All Books"
declared `matchPath: '/library'` and therefore matched *every* `/library` URL.
Selection is now computed by `isSubItemSelected()`, which compares the parsed,
decoded `search` param. That matters because `Library.tsx` settles the URL into
`?search=read_status%3Ain_progress&page=1`; a raw string comparison against
`item.path` would still fail on the percent-encoded colon and the appended
`page`.

**The click was discarded.** `Library.tsx` suppressed echoes of its own URL
writes with a one-shot `isInternalUpdate` boolean, and that boolean got
permanently stuck at `true`. The write effect lists `setSearchParams` in its
dependencies, and react-router rebuilds that callback whenever
`location.search` changes — so the effect re-fired on URL changes it had not
caused, re-arming the flag each time. Once it wrote an identical query string
the URL stopped changing, the sync effect stopped running, and nothing ever
cleared the flag. Every later external navigation was then swallowed as a
phantom "internal echo": the incoming `search` was dropped and the write effect
rewrote the URL back to `page=1`.

The tell was that "All Books" kept working from the same machinery — its
`reset=1` branch is checked *before* the guard, while `search` is read *after*
it.

The flag is replaced by `lastWrittenSearch`, which records the query string
actually written and compares it to the incoming one. That is idempotent, so
repeated writes of the same URL are harmless and a genuinely different URL
always gets through.

The backend was never at fault: `buildFieldFilters` → the `filters` param →
`PerUserFilters` was correct all along, but `parsedSearch` was never populated
to feed it.

Covered by `Sidebar.test.tsx` (11 cases), including the percent-encoded
settled URL and an invariant that exactly one sub-item is selected for any
given Library URL.
