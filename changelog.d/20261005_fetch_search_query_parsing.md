### Fixed

- **Metadata fetch: filename-shaped titles now search by the book's name.**
  - New `metadata.ParseBookName`, one parser for the title shapes file and
    folder names carry: rip tails ("(Narrator) 64k 12.07.23 {345mb}",
    trailing `[tags]`), " - Unknown Author", " - read by X", a leading
    release year ("2018 - Blueshift"), a leading author segment (the book's
    author, an authority-known author, or an author-shaped ancestor folder),
    a trailing author ("Title - Author"), series slots ("Series - NN -
    Title", "Series NN - Title" with one-word and hyphenated series, "Series
    Book NN", a sub-series field) and a track suffix after a slot
    ("... - 01", "(1 of 3)"). The scanner's folder parse
    (`ExtractMetadataFromFolder`) and the search's `parseSearchTitle` both
    use it; the folder parse no longer titles a book by the text before the
    first " - " ("2018", "Discworld 24").
  - Root cause of the two census books Audible finds but the app recorded no
    match for ("2018 - Blueshift", "Discworld 24 - The Fifth Elephant - 01"):
    the search parser, not a filter and not the 4-variant cap. The bare name
    was never asked. Two rules downstream were also wrong for these titles;
    each is fixed with a test that fails on the old rule: the series-number
    tiebreak read the raw title's trailing " - 01" as the book's number
    (penalising the edition at the real position 24), and the strong
    criteria took a stripped track number as one of the book's own numbers
    (keeping "The Fifth Elephant, Part 1" as book 24's candidate).
  - Person matching in the search folds accents, so "Zoë Brontë" vouches
    for a book tagged "Zoe Bronte".
  - An author equal to the title ("Hammer Fall Rising" by "Hammer Fall
    Rising") is dropped as a search hint; with no person left to vouch, an
    answer to a question asked without an author must carry exactly the
    book's title and all answers must name one author. A title that is an
    authority-known person's name (or its slug) AND names one of the book's
    own people (or the book has no real author) is refused so the
    transcribed title or folder stands in; a biography titled by its
    subject is still searched.
  - `authorjunk.IsGenreTagline` also reads a tagline with one coined word
    ("A Daopocalypse Progression Fantasy", "A Deck-Building LitRPG"), so a
    slot followed by one is never taken as the book's name.
  - Scan-time folder parse, measured over the library's 75,390 paths: 3,247
    folder titles differ from the old parse; the new one equals the stored
    title for 659 of them, the old one for 165.
  - A "<title> - 02" row beside "- 01" and "- 03" in one folder is now
    recognised as one file of a set and skipped, before the new track-suffix
    strip could hand it the whole book's candidate.
  - Live Audible check over the census's 20 filename-shaped samples: 0/20
    top-1 matches before, 10/20 after (the 10 Audible carries).

### Added

- **Scheduled candidate fetch for books never fetched.** New
  `candidate_fetch` scheduler task (`scheduled.candidate_fetch.interval`,
  default 360 min, 0 turns it off) enqueues `metadata.candidate-fetch` with
  `unfetched: true`. The op selects live primary, unapplied books not marked
  "no match" that have no candidate-cache row (never fetched, or
  invalidated) or an empty row recorded for questions a search no longer
  asks, skips books with no usable search title, checkpoints the selection,
  and fetches only: no candidate is applied and no book is written. Its
  rate limiter and workers are sized from the enabled sources' budgets
  (Audible 8/s).

### Changed

- **The new folder parse never rewrites existing books on a rescan** (owner
  decision 2026-10-05: search and new imports only). A rescan holds an
  existing row's title, author and series wherever the scanned value came
  from the folder parse (`scanner.folderDerivedLocks`); tag values still
  land as before, and new rows are created from the new parse.
- **New repairs fixer `maintenance.reparse-folder-names`.** Lists existing
  books whose title, author or series is what the OLD folder parse wrote
  (`metadata.LegacyFolderParse`, a frozen copy used only as evidence) and
  that the new parse reads differently, one row per field with before and
  after. Locked fields are held; iTunes-owned and Doctor Who / Big Finish /
  Torchwood rows are skipped by the framework guards; a proposed title that
  is the book's own author is not a row. Apply writes approved title rows
  (journaled and locked) and series links to existing series rows; author
  rows are review-only. Offline over the library's 75,390 census paths:
  178 title rows and 2 series rows on 180 books, 0 author rows.
- **A first field that only repeats an ancestor folder is a series, never
  an author** (`Star Wars/Star Wars - Thrawn` reads series "Star Wars", no
  author; an earlier draft credited "Star Wars" as the author). An author
  needs person evidence: the book's own author or the authority lists.
- **A folder segment repeating its folder's name is the author when it is a
  person** ("Brandon Sanderson/Brandon Sanderson - Elantris" credits
  Brandon Sanderson; "Agatha Christie - Poirot - The ABC Murders" reads
  series Poirot, title The ABC Murders) and the series only when it is not:
  a curated franchise (Doctor Who), a series the library already has (Star
  Wars, Harry Potter, Sherlock Holmes, Jack Reacher) or no person shape
  (Warhammer 40k). The scanner and importer pass that evidence
  (`scanner.FolderNameEvidence`: authority person lists, author rows, series
  names) through `metadata.AssembleBookMetadataWith`.
- **A rescan of an existing book creates no rows from the new parse.** The
  hold now runs before any author, series or work is resolved or created,
  and also keeps the stored narrator and series position.
