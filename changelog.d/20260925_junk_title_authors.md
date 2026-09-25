### Fixed

#### `maintenance.author-strip-merge` now catches authors that are their own book's title

Some author rows were never a person at all — the importer filed the book's
title where the artist tag was missing, so the author name became the title
itself ("Arcane Chef 2" crediting "Arcane Chef 2: A LitRPG Adventure",
fixed by hand as author id 64477 on 2026-09-25). The op now deletes a row as
junk when its name matches every live book it credits (the whole title, the
title's leading segment before `:` or ` - `, or `<series> <position>`),
comparing case-, punctuation-, `_`- and whitespace-insensitively. An author
with even one differently-titled book is left alone — that difference is the
only evidence distinguishing a corrupt row from a real person who happens to
share a title with one of their own books. Gated by the existing `delete_junk`
flag; report-only by default like the rest of the op.
