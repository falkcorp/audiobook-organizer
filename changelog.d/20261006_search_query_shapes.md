### Fixed

- **Metadata search asks the catalog for the book, not for junk around it.**
  A forced candidate re-fetch on 2026-10-05 left 586 of 732 books with no
  match. Run through the real query path, 238 of the questions asked carried
  a shape no catalog answers; after this change none do. The shapes, as
  made-up rows, are `internal/metabatch/testdata/nomatch_shapes.tsv`.
  - Author credits that no catalog carries are cleaned where the hint is
    sent and hashed (`metafetch.SearchAuthorHint`): release-group tags,
    a `zz` sort prefix, file-copy suffixes (`_copy1`, `_10-02`), HTML
    entities and `(Unabridged)`. A bracket is a note on the credit: the name
    outside it is the author (`Homer [Fagles]` is `Homer`); only an empty or
    studio remainder hands it to a bracketed person
    (`GraphicAudio [Jane Example]`).
  - The apply gate and the batch verdict accept a row hashed with the raw
    or the cleaned author. A no-author hash proves a credit by itself only
    for the system's placeholders; any other credit that cleans to nothing
    proves itself through the row's search fingerprint.
  - Rows fetched before this change keep their candidates: their
    fingerprint is matched as the same book's questions read the old way,
    or with only the suspect-author question different, and an authority
    read fault is no change. Caveat: such a row's "nothing found" is
    re-asked once (only empty rows; Google Books stays within its daily
    fallback budget).
  - An author credit that restates the title's words is judged on evidence.
    It is dropped only when proven junk: a non-person-shaped credit whose
    author row's other books all restate it (a series filed as an author),
    or a number or genre-tagline shape. It is kept for a possessive
    (`Tom Clancy's …`, `Rick Steves' …`) or a credit segment. Otherwise
    -- an authority-known author, a person whose books all carry their name,
    a person with other books -- it is suspect: still sent, with the title
    also asked alone. An authority read error never drops an author. A
    dropped credit is replaced only by an ancestor folder the authority
    lists know as an author and not as a narrator.
  - A title that is a known author is swapped with its credit only when the
    stored credit ends in a file-copy or disc-track suffix whose remainder
    is not person-shaped. A sort prefix, a release tag or a person-shaped
    remainder is no evidence, so biographies (`Steve Jobs` by Walter
    Isaacson) are asked as stored, by every caller.
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
