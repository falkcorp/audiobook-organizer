### Fixed

#### `/search` narrators no longer emit hardcoded numBooks: 0

The `/api/libraries/:id/search` endpoint was emitting narrator objects with `numBooks: 0` to match a misread of the ABS specification. Per the contract and the existing `/api/libraries/:id/narrators` handler's correct implementation, the field must be omitted entirely when unknown, not emitted with a false value. Narrator objects in search results now match the correct shape by omitting the numBooks field.
