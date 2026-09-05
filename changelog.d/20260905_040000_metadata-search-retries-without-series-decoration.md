### Fixed

- **Metadata search no longer gives up on a series-decorated title.** Library titles
  such as "Eternal Dominion, Book 04 - Assertions" or "Path Of The Voidwalker - BK07"
  carry a series slot no provider indexes, so the literal query came back empty from
  all four providers and the book was recorded as not found: 73 of the first 100
  books in the 2026-09-05 bulk fetch, each a real, findable book. Both the bulk fetch
  and the review dialog's lookup now retry, only after the literal titles miss, with
  the decoration stripped and with each side of the subtitle separator ("Eternal
  Dominion", "Assertions"), stopping at the first variant that answers. A book the
  literal query finds costs no extra provider call.
