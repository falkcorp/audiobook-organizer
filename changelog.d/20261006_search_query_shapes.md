### Fixed

- **Metadata search asks the catalog for the book, not for junk around it.**
  A forced candidate re-fetch on 2026-10-05 left 586 of 732 books with no
  match. Run through the real query path, 238 of the questions asked carried
  a shape no catalog answers; after this change none do. The shapes, as
  made-up rows, are `internal/metabatch/testdata/nomatch_shapes.tsv`.
  - Author credits that no catalog carries are cleaned where the hint is
    sent and hashed (`metafetch.SearchAuthorHint`): release-group tags
    (`[XYZ]`), a `zz` sort prefix, file-copy suffixes (`_copy1`, `_10-02`),
    HTML entities and `(Unabridged)`. A bracket holding the only person
    (`AudioHouse [Jane Example]`) gives the author. The apply gate and the
    batch verdict accept a row hashed with either the raw or the cleaned
    author, so no cleaned book reads as identity-stale or is re-asked on
    every run.
  - An author credit that restates the title's words is judged on evidence.
    It is dropped only when it is proven junk: an author row whose other
    books all restate it (a series filed as an author), or a shape no
    person has (a number, a genre tagline). A real author is kept: a
    possessive (`Tom Clancy's …`), a credit segment
    (`Brandon Sanderson - Mistborn`), an authority-known author, or a
    person with other books. A credit that is merely suspect is kept, and
    the title is also asked alone. An authority read error never drops an
    author. A dropped credit is replaced only by an ancestor folder that
    the authority lists know as an author and not as a narrator.
  - A title that is a known author, with a book name in the author field,
    is asked the other way round, but only with positive junk evidence on
    the author side. Biographies (`Steve Jobs` by Walter Isaacson) are
    asked as stored.
  - `metadata.ParseBookName` (search and new imports) now reads:
    - the organizer's own `NNN - <Title> - read by narrator` file template,
      when the number is zero-padded or the file's track folder is named
      for it;
    - a trailing `(2017)` year;
    - strictly terminated HTML entities;
    - the `꞉` and `_ ` colon stand-ins.

    A bare `copy1` title is unsearchable, and `A LitRPG Apocalypse` is a
    genre tagline.
  - The candidate search climbs one folder out of a `read by narrator` or
    `Unknown Title` folder that the organizer named from a bad title.
  - Stored titles and authors are not rewritten. Every fix changes the
    title or author asked, or adds a title-only question, so the search
    fingerprint re-asks exactly the affected books with no
    `searchInputVersion` bump.
