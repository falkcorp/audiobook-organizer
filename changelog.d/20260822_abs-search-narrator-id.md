### Fixed

#### `/search` narrator results now carry the non-optional `id`

Narrator objects in `GET /api/libraries/:id/search` were emitted as `{"name": ...}` with no `id`. Contract §6.3 records that the target client's `Narrator.id` is non-optional and that a single element without it throws the entire list, so search results containing any narrator could break the client's decode. The id is now derived from the name exactly as the narrator list and real ABS derive it.

`searchResponse.Narrators` is also retyped from `[]any` to `[]narratorDTO`. The untyped slice is why this element could drift from the canonical DTO in the first place.

The regression test previously asserted only that `numBooks` was omitted, which pinned the incomplete shape as correct; it now asserts the derived id as well. The search oracle fixture records `narrators: []`, so conformance passes vacuously here and cannot catch element-level omissions on its own.
