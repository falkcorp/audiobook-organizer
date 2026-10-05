### Added

- One shared franchise matcher (`internal/franchise`) for Doctor Who, Big
  Finish and Torchwood. It replaces the separate patterns in the bulk-apply
  guard and the Repairs guard, and it adds the range terms from the
  2026-10-04 census (War Master, Stargate when Big Finish context is present,
  BBV, Short Trips, Companion Chronicles, Lost Stories, Kaldor City, Dark
  Shadows, Cybermen, Zygons, Star Cops, TARDIS, Big Finish download folders,
  and more), each with its exclusions ("War Master's Gate", "Castle of Dark
  Shadows", Stargate novels from other publishers).
- The bulk-apply guard now reads a book's narrator, publisher, author credits
  and franchise tags. The Repairs guard now reads the transcribed fields and
  franchise tags. A book tagged `franchise:*` stays held after its title or
  path changes. Both guards only hold more books than before, never fewer.
- A new Repairs fixer, `maintenance.tag-franchise`, tags each matched book
  with `franchise:<name>` and `range:<range>` (source `franchise-matcher`) and
  shows the signals that matched. Rows backed only by weak evidence are
  review rows. It writes tags only, never a file, a book field or the iTunes
  library, so iTunes books are tagged too. Every tag is journaled, and the
  apply operation's revert removes them.
