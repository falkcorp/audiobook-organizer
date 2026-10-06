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
    metadata apply: fill-only, field locks honoured, change history recorded
    after the write, the match stamped. "Undo last apply" reverts it. Title
    and author are not written (the row requires the primary to hold them
    already), and no rename or tag work is queued.
  - `candidates_twin`: the twin's cached candidates are copied onto the
    primary for review, re-keyed so the primary's own identity check accepts
    them. Nothing is applied. The copy is journaled after it lands.
  - Held, each with its reason: twins that disagree, a different edition
    (runtime more than 5% apart, abridged vs unabridged, different
    narrator), locked fields, iTunes-linked or not-ABS-listed primaries, a
    different title or author, an ASIN conflict, a twin record that can no
    longer be recovered from its cache, a record also carried by a book in
    another group (it would trigger a cross-group duplicate election), a
    group with no single primary, and Doctor Who / Big Finish / Torchwood.
  - The fixer never changes which book is primary. Apply re-checks each row
    (changed since plan), and the metadata apply re-checks the primary under
    the book's write lock. That uses the new `metafetch.ApplyOptions.Guard`.
  - New `metafetch.CandidateSourceHash` and
    `metafetch.Service.CopyCandidateCache`.
