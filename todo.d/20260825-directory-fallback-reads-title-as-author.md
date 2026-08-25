## The directory author fallback reads the TITLE as the author on organized books

`extractAuthorFromDirectory` (present in BOTH `internal/metadata/metadata.go` and
`internal/scanner/scanner.go`) takes the file's **immediate** parent directory as
the author.

The organizer's own layout is `<root>/<author>/<title>/<file>`. So for an
organized book the immediate parent is the **title**, and the fallback attributes
the book to an "author" that is really its own title.

Measured 2026-08-25:

```
extractFromFilename("/mnt/bigdata/books/audiobook-organizer/Unknown Author/Some Book/Some Book.mp3")
  -> Artist = "Some Book"
```

This is a strong candidate for the junk-author census in
`docs/audits/2026-08-25-unknown-author-feedback-loop.md`: 4,643 of 17,947 author
rows (25.9%) are not people, and many are plainly titles — `Rings Haven`,
`The Sapphire Crescent`, `Avatars Dance 1`, `19 - Apocalypse`. Every one is a
non-nil `AuthorID` that closes the AI nomination gate, so each mis-attributed
book is locked out of ever being corrected.

Compounded by `CreateAuthor` being racy
(`todo.d/20260825-createauthor-check-then-create-race.md`): each junk name also
mints one or more real author rows.

- [ ] Decide the correct rule. The author is the **grandparent** under the
      organizer's own layout, but an unorganized import may legitimately have the
      author as the immediate parent. It likely needs to be layout-aware (is this
      path under `RootDir`?) rather than positional.
- [ ] Apply it in both copies, or collapse the two parsers into one — they are
      already divergent copies of the same logic.
- [ ] Quantify how many of the 4,643 junk author rows came from this specific
      path before deciding on a repair.

Found while fixing the `Unknown Author` placeholder loop; deliberately out of
scope there.
