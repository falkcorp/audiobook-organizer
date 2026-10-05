- [ ] **AUTH-LISTS-PR3** Authority lists PR 3: catalog-harvest feed. After
      `catalog.harvest-authors` stores new `cat_raw:` payloads, rebuild the
      authority lists (or ingest the harvested products incrementally through
      the `ref_src:` ledger), plus LookupProduct for owned-ASIN books missing
      from the catalog at 8 req/s.
- [ ] **AUTH-LISTS-CONSUMERS** Wire the remaining authority-list consumers,
      one at a time behind the same flag, each loading one Snapshot:
      at import (authorcredit.Resolve in the file importer, scanner, iTunes
      and metafetch apply; the narrator splitter; CleanGate publisher
      refusal); in backfill and repair (combined-author fixer, junk-author
      fixers, narrator cleanup/split, the credits backfill of the credits plan
      PR 3, and new publisher normalisation: merge publisher spellings via
      `CanonicalPublisher`, pull publishers out of author fields); and in
      metadata matching (filename/folder token roles via `ClassifyTokens`,
      which also flags transposed title/author books; metafetch candidate
      scoring, known person in the right role up and cast in the author field
      down; provider search by `AuthorASINs` instead of a fuzzy name; later
      the catalog as an offline match target).
- [ ] **AUTH-LISTS-TIER-C** Ingest tier-C single whole values from the
      joined caches (metadata_cache, metadata_fetch_cache, metadata_state)
      once credits PR 2 makes them lossless; never re-split joined strings.
