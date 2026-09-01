### `SplitCompositeAuthorName` has no `" with "` branch

`"Bill Clinton with James Patterson"` returns no split — on `origin/main` and on
`fix/person-name-unicode` alike. `" with "` is a real co-author credit form on
audiobook covers, and it is not in the separator list (`/`, `,`, brackets, `;`,
` and `, ` & `).

What happens instead: the string falls through to `trySplitConcatenatedAuthors`,
which tries every word boundary. Before #3029 that could place the boundary
*inside* the phrase and mint a left half containing the word itself —
`"Volker Kutscher with Bob"` was a measured example, one of 253 such strings.
#3029 stops those being minted (the shared predicate rejects an interior
lowercase non-particle), so the current behaviour is a **missed** split rather
than a wrong one. That is the intended direction, but the credit is still lost.

Fix is to add `" with "` to the separator branches with the same
`personname.LooksLikePersonName` gate every other branch now uses. Care needed on
two shapes before doing it:

- `"X with Y"` where `Y` is not a person (`"Coffee with Milk"`) must refuse.
- Titles legitimately containing " with " must not be split — the branch must gate
  on both halves being person-shaped, and refuse the whole split otherwise, the
  way the comma branch does.

Measured 2026-09-01 while running the consumer differential for #3029; out of
scope there because it is pre-existing on both sides and adding a separator
changes behaviour the differential was measuring.
