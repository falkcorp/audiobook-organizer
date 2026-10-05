### Fixed

#### Author credits: follow-ups to the combined-credit fixer (#3717)

- One person written surname first is never split or refused as a combined
  credit: "Le Guin, Ursula K.", "Van Vogt, A. E.", "Tolkien, J. R. R.",
  "Martin, George R. R.". A suffix or degree is part of the name before it:
  "Martin Luther King, Jr.", "Rob J. Hayes, M.A.".
- A doubled name, or an author paired with its own alias, links that one
  author. Examples: "A. G. Riddle, A. G. Riddle"; "Robert Galbraith, J. K.
  Rowling" when Galbraith is Rowling's alias.
- The iTunes importer resolves credits through the same shared helper as the
  other paths. The file importer never creates an author on a plain miss: the
  book stays authorless, which is the behaviour before #3717.
- A leading byline ("By: Brandon Sanderson", "by Brandon Sanderson") is
  stripped before every creation gate and author lookup. If the byline leaves
  one bare word ("By: Zork"), that word is linked when it is already an author
  or alias. Otherwise it is refused and never created.
- A single-word pen name ("Shirtaloon, Travis Deverell") splits at import when
  every part is already an author or an alias. In the combined-credit fixer, a
  single-word part can also be created when a metadata provider credited
  exactly that word to the book. Byline and single-word rows are new classes
  (`by_prefix` and `single_word_name`) and are always review rows. An unknown
  single word without a provider credit stays `skipped_split_refused`.
- Combined-credit fixer proposals, from the prod dry run of 2026-10-04:
  - A new author named by two records of one book is proposed once.
  - Credits that name one person (same letters, or an alias) are kept once, at
    the first position, and the row's reason says which credit was dropped.
  - A part that still holds a separator ("Sarah Lin/Travis Baldree") is split
    again and is never created as one author.
  - The primary record's order of names decides the order and breaks ties at
    one position, so the primary stays the first name. A credited part of a
    primary-only record keeps its place.
  - A credit of an author id that no longer exists is dropped, and the reason
    says so.
  - A new part one letter away from an author the book already credits
    ("Artur C. Clarke" beside "Arthur C. Clarke") is held as
    `skipped_ambiguous_author` instead of being created.
- Review of #3729:
  - A credit whose every part is an existing author links all of them in
    order, whatever the number of parts and even when a part is also a series
    or book title ("Michael Anderle, Craig Martelle"). The part cap and the
    title check guard only new authors. Before this, iTunes imports of such
    credits got no author.
  - A real primary author tied at one position with another credited author
    stays first. A combined-credit row whose rewrite would not keep the
    primary at position 0 is held as `skipped_primary_not_first`.
  - The file importer (and every Deluge auto-import) creates a new single
    person-shaped author as before. Only a credit of several names, none of
    which exists, leaves the book authorless.
