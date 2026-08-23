### Fixed

#### `/search` narrator results now carry a non-optional `id` that actually resolves

Narrator objects in `GET /api/libraries/:id/search` were emitted as `{"name": ...}` with no `id`. Contract §6.3 records that the target client's `Narrator.id` is non-optional and that a single element without it throws the entire list, so any search matching a narrator could break the client's decode.

The names now come from the same cached contributor index that backs `GET /narrators`, not from the raw store. That is what makes the id usable rather than merely present: the index splits compound credits and covers visible books only, so a search hit carries the same id the Narrators tab publishes and the `narrators.<id>` filter resolves. Sourcing from the raw store would emit a well-formed, cleanly decodable id for a person who does not exist — the stored credit `"Jeff Hays, Annie Ellicott"` is two people — giving a search hit that opens onto an empty list. `/search` already read this index for authors; narrators were the last contributor list still on the raw store.

`searchResponse.Narrators` is retyped from `[]any` to `[]narratorDTO`, so the compiler now enforces the element shape that drifted. `numBooks` is still omitted, preserving the previous fix.

Two regression tests: one pins both halves of §6.3 on the element, and one asserts the id `/search` publishes equals the id `/narrators` publishes and then resolves through the filter. The latter is the assertion that catches the compound-credit case; the search oracle records `narrators: []`, so conformance passes vacuously and cannot see element-level defects at all.
