### Changed

- Author credits: a credit part named like a book or series ("Dragon Born",
  "Michael Anderle") no longer links because its author is credited on a book
  with no series. Only a book in a different, named series counts as evidence
  now, alongside an exact provider credit (owner decision 2026-10-05).
- Author credits: person evidence now has a strength. Weak evidence (a credit
  in a different series, a provider credit) links such a part but never makes
  it the primary author. Strong evidence lets it be the primary: the authority
  lists hold the name as an author from the owner's own library (tier O) or
  with an Audible contributor ASIN. Strong evidence keeps the credit order and
  does not move a part forward.
- The authority lists feed author-credit resolution through the server's
  store when the new setting `authority_evidence_enabled` is on. It is off by
  default, and credit resolution is unchanged while it is off. The lists are
  held in memory, loaded in the background, and reloaded every 10 minutes.
  A resolve never reads them from the store.
- The "not crediting" line for a dropped credit part is logged at Debug, not
  Info, so rescans no longer repeat it.
