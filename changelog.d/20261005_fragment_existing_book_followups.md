### Fixed

#### Fragment fixer: existing-book follow-ups

- A genre or edition subtitle ("A Novel", "A Thriller", "A LitRPG Adventure",
  "Unabridged") is never the key a title is matched on: "Fahrenheit 451: A
  Novel" and "1984: A Novel" no longer both key as "anovel", and "Origin: A
  Novel" matches "Origin". A number before such a subtitle stays part of the
  name.
- A join never picks a book that a no-parent apply of this fixer assembled
  (the survivor of an unreverted plan record) while another agreeing book
  exists; when only such a book agrees, the row is held with "revert that
  apply first". Among the rest the target is the book with the most file
  rows, then the longest total.
- A plain no-parent row checks the whole library again for a book of its
  title at apply time; one created since the plan refuses the row
  (changed since plan) instead of letting it assemble a second copy.
- A joined fragment's listening position lands at the start of the target's
  file at the same chapter position (else its share of the set scaled to the
  target's length), not at its running sum among the fragments.
- A join target with an iTunes id on any file row or as an external id (or
  whose external ids cannot be read) makes the row manual-only, as one on the
  book already did.
- The co-owner and existing-book checks share one owners map, each book once
  per path; an unplaced fragment that is also hands-off is counted as
  unplaced.
