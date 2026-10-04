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
