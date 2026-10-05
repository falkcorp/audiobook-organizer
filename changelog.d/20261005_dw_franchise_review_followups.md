### Changed

- Franchise tagger review follow-ups:
  - A title named only by a census range term ("Genesis of the Cybermen") is
    now weak evidence, so its tag row is a review row. The guards still hold
    the book.
  - Owner decision 2026-10-05: in the bulk-apply guard's narrator and
    publisher checks, a credit named only by "Missy" ("Missy Cambridge") no
    longer holds a book. "Missy" in a title, path or series still does.
  - A Repairs fixer that declares itself tags-only now gets a writer that
    refuses every other write.
  - Each tagger apply stamps its own tag source (`franchise-matcher:<op id>`),
    so reverting one run never removes a tag another run added.
  - A book tag and its index entry are now written and removed together, so
    a failed write cannot leave an untracked tag.
  - The metadata upgrade now uses the full bulk-apply owner-manual guard
    (author credits, franchise tags, narrator, publisher, transcribed fields,
    file rows). iTunes regroup, iTunes clone-into-library, author-path-link
    and repoint-version-primary now check every field of the book row
    (path, title, series, narrator, publisher, transcribed title and author),
    but not author credits, tags or file rows.
