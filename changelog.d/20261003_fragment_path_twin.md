### Fixed

#### Fragment consolidation — a fragment at the exact path of a matched fragment adopts its match

Two single-row books registered for one file ("02" beside "Eldest - 02")
carry one file's evidence between them. The one without evidence of its own
(no import history, no hash, size unknown) matched no parent row, fell out of
the plan silently, and stayed a live co-owner of the path; the owner check
then refused its sibling's row as "also owned by book". On 2026-10-03 this
blocked 21 of 48 applicable rows (~800 fragment books). The evidence-less
twin now adopts the single, unambiguous match of the fragment at its path,
named in its evidence, so both retire in one row. A path shared by two
matched fragments, or a donor whose own match is ambiguous, lends nothing.
