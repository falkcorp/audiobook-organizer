- [ ] **Organize renames with placeholder metadata — "Unknown Author" gets
      baked into directories and filenames.** Observed in a Review Organize
      preview (2026-08-14): a book under
      `audiobook-organizer/Unknown Author/All Jobs and Classes!…_ LitRPG/Epic Progression/`
      was offered a rename to
      `…/Unknown Author/All Jobs and Classes!…/… - Unknown Author - read by Ryan Dimon, Arielle Noelle.m4b`
      — the path cleanup itself worked (series-suffix folder collapsed), but
      the placeholder author was written into BOTH the folder and the new
      filename, twice. The book plainly has usable metadata (full title,
      two narrators) so an author lookup would very likely resolve.
      Wanted behavior for anything in the organizer tree:
      1. If the author is a placeholder ("Unknown Author"/empty), organize
         must NOT bake it into the target path. Resolve metadata FIRST
         (metadata fetch by title/narrators/tags), and only rename once a
         real author exists;
      2. otherwise route the book to review flagged "author unresolved"
         instead of proposing a rename that cements the placeholder;
      3. the rename template should always be built from resolved author +
         metadata, never from whatever the current row happens to hold.
      Audit how many books already have "Unknown Author" baked into their
      organizer-tree paths while carrying resolvable metadata.
