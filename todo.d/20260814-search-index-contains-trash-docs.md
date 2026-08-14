## Bleve index holds ~3,953 docs for soft-deleted books

The 2026-08-13 search-index repair backfilled 16,738 "missing" books using
the pre-#2408 leaky `ListBookIDs` enumeration — which included the trash.
Post-fix boot log (2026-08-14 10:01): `search index coverage OK
indexed=67824 books=63871`. DocCount − live = 3,953 = the soft-deleted set
exactly, so trashed books are now indexed and plausibly reachable through
web search until the index is reconciled.

- [ ] Add/verify a reconcile pass that REMOVES index docs whose book is
      soft-deleted or gone (the coverage gate only checks
      `len(ListBookIDs()) <= DocCount()`, which a polluted index passes
      forever — already noted in 20260813-search-index-repair-prod-findings
      as the "two slightly different populations" item; this is a live
      instance, not a hypothetical).
- [ ] Verify with a bogus-value control: search for a known trashed title
      before and after the cleanup.
