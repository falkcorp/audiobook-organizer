### Added

- **New repair: copy metadata to a version group's primary from its twin
  (`maintenance.version-twin-metadata`, /review Repairs tab).** The
  2026-10-05 census found about 859 primary books counted as "metadata
  missing" while another version of the same book was already applied, or
  held fetched candidates. Nothing copied review status or candidates across
  a version group. One row per version group, applied by explicit row id
  with dry run, like the other fixers.
  - `applied_twin`: the twin's applied record is recovered from its
    candidate cache (the candidate whose source hash equals the twin's
    `metadata_source_hash`). It is applied to the primary through the normal
    metadata apply: fill-only, field locks honoured, the match stamped.
    Its change history is recorded after the write under the batch id
    `<op id>:<primary id>`, sourced to the fixer, and a failed history write
    is reported as a partial apply. It writes nothing outside that batch: no
    `metadata:source`/language or category tags, no fetched-value
    provenance, no segment titles, no identifier backfill, no cover
    download (`ApplyOptions.BookRowOnly`). One `metadata_apply` op-journal
    row is written after the commit, so the op revert (Operations page)
    undoes the batch, as does "undo last apply". Title and author are not
    written (the row requires the primary to hold them already).
  - Narrator, ASIN, ISBN, abridgement and runtime name one edition; they are
    copied only with evidence that the primary is the twin's edition (known
    runtimes within 1%, the record's runtime too when it has one, or the
    primary already carrying the record's narrator). Without it they are
    left out and the rest of the record is applied.
  - `candidates_twin`: the twin's cached candidates are copied onto the
    primary for review, re-keyed so the primary's own identity check accepts
    them. Nothing is applied. The copy is journaled after it lands with the
    primary's prior cache state, and the op revert removes it again.
  - Held, each with its reason: twins that disagree, a different edition
    (runtime more than 5% apart, abridged vs unabridged, different
    narrator), locked fields, iTunes-linked or not-ABS-listed primaries, a
    different title or author, an ASIN conflict, the record's ASIN on a live
    book outside the group (or the ASIN index not built yet), a twin record
    that can no longer be recovered from its cache, a record also carried by
    a book in another group (it would trigger a cross-group duplicate
    election), a primary whose own fetch recorded "found nothing", a group
    with no single primary, and Doctor Who / Big Finish / Torchwood (on the
    books, the twin's record, or any copied candidate).
  - The fixer never changes which book is primary and writes no other book.
    Apply re-checks each row (changed since plan), and the write re-checks
    the primary under the book's write lock (`metafetch.ApplyOptions.Guard`,
    and the cache copy's check): still the group's live primary, still
    unapplied, and not iTunes-linked by any signal the plan reads (book or
    file iTunes id, file iTunes path, live iTunes external id, iTunes
    library path), and no book outside the group has gained the record. The
    MATCH-4 duplicate election, which demotes and merges books outside the
    survivor's group, does not run for this fixer's apply
    (`ApplyOptions.SkipHashElection`).
  - New `metafetch.CandidateSourceHash`, `metafetch.Service.CopyCandidateCache`,
    `metafetch.Service.UndoApplyBatch`, `ApplyOptions` `Guard`,
    `SkipHashElection`, `BookRowOnly`, `HistorySource` and `RequireHistory`,
    and the op-revert change types `metadata_apply` and
    `metadata_cache_copy` (`internal/undo`).
