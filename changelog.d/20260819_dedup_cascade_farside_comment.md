### Changed

- Corrected the justification comment on the dedup-candidate cascade's
  corrupt-record branch. It claimed a leftover far-side entity-index entry would
  be cleaned up by that book's own deletion, which is not guaranteed — the book
  may never be deleted. The entry is in fact inert: the entity index has exactly
  two consumers and both re-resolve every hit through the candidate record,
  skipping anything that does not resolve. The comment now states that, and warns
  against adding a consumer that trusts the index directly.
