### Fixed

#### Requests the server couldn't read no longer run as "do everything"

Wave 2 of the silent-failure sweep. Seven endpoints threw away the error from
parsing your request body. When a body failed to parse, every setting in it fell
back to its default — and on these endpoints the defaults are the *wide*,
*destructive* ones, not the safe ones.

**Bulk merge** was the worst. Every field in its request narrows which duplicate
candidates get merged. If the body couldn't be read, all of them were dropped,
the defaults filled in "all pending book candidates", and the query ran with a
limit of 100,000. A request to merge one narrow group could merge every pending
duplicate in the library. Merges are the hardest thing here to undo.

**A dry run could turn into a real run.** Maintenance jobs and the bulk metadata
write both take a "preview only" flag. An unreadable body left that flag off —
which means *do it for real*. The response looked identical either way, so
someone asking for a preview got a mutation with no way to tell.

**Bulk metadata write failed twice at once**: the preview flag fell off *and* the
author/series filter fell off, so a request scoped to one author became a real,
unfiltered write across the entire library.

Also fixed: **backup retention** (an unreadable value silently used a different
retention count, and rotation deletes), **author splitting** (explicit names you
supplied were dropped and the server split the author its own way instead),
**segment-scoped metadata writes** (losing the segment list is what *unlocks* the
whole-book file rename), and **metadata search** (an unreadable search wrote into
the shared cache under a key that looked like a legitimate empty query, so later
real searches could read it back).

An empty body is still fine everywhere it was before — that is a normal way to
call several of these. The change is only that a body we *cannot read* is now
refused instead of being treated as though you had asked for the maximum.

Three similar-looking spots were deliberately left alone: there, the default is
"preview only", so failing to parse already lands on the safe side. A test now
records that as an intentional decision rather than an oversight.
