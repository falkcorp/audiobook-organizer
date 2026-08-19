### Fixed

#### Narrowing an interface by listing its methods made the width gate red

`interfacebloat` counts **declared entries**, not transitive methods. Replacing a
body of four `database.*` embeds with the nine methods actually used cuts the method
set from 115 to 9 — and raises the declared width from 4 to 9. Two interfaces crossed
the limit that way and the ratchet went 3 → 5.

`reconcile.Store` is now a composition of four focused interfaces (`bookReader`,
`bookWriter`, `importPathReader`, `operationRecorder`) holding the same nine methods.

`handlers/metadata.MetadataStore` was worse: it was **already** a correct composition
of six focused interfaces, and the only thing wrong with it was one embed —
`database.BookStore`, 51 methods. Flattening it to a method list destroyed a good
split to fix a single line. It is restored as the composition, with the embed
replaced by a four-method `MetadataBookStore`. `MetadataRejectionStore` is also
restored: an unbounded string replacement had appended four methods to it as well,
because its closing lines matched the same pattern.

Mocks for `MetadataStore` and `PlaylistStore` regenerated.
