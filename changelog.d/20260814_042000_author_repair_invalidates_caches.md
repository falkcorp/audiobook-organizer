### Fixed

#### Author repairs are visible on the author list immediately

`maintenance.author-conjunction-repair` now invalidates the cached author list
(and the author-duplicates dedup cache) after a run that wrote.

Without it the repair was invisible on the one page anyone would check. The
2026-08-14 apply landed correctly — 30 authors merged, 15 renamed, and all 145
affected book records verified to carry the corrected links — while
`/api/v1/authors` kept returning the pre-repair names. That cache holds a
**24-hour TTL** and was invalidated only by the interactive entities API, so a
maintenance op that renamed or deleted authors left the list stale for up to a
day. Reading it straight after the apply showed 48 stranded rows still present
and nothing deleted, which reads exactly like a repair that silently did
nothing.

`ServerDeps` gains `InvalidateAuthorsCache()` alongside the existing
`InvalidateDedupCache()`, so any maintenance op that mutates authors can now say
so.

A dry run deliberately does **not** invalidate: it changed nothing, and dropping
a warm cache costs real work for no reason. Nor does a run that matched rows but
wrote none. Both cases are covered by tests, and both guards were
mutation-tested — removing the invalidation reds the apply case, and widening it
to fire unconditionally reds the dry-run case.
