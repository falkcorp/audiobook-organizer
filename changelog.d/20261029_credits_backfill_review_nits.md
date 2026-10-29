### Fixed

- Credits backfill: an apply that cannot read another operation's journal row
  (a row id that is not a ULID, a row that does not decode, an unreadable judged
  marker) now says how to proceed: discard that operation, and what that costs.
  The 30-second leftover window now has boundary tests at 29 s and 31 s on the
  intent row, the created-narrators row and the operation's end, and the code
  documents what the window covers and misses, why a revert does not clear the
  judged marker, why a discard needs no clearing, why the leftover delete does
  not use `require_no_name_match`, and the scale of the operation-history listing.
