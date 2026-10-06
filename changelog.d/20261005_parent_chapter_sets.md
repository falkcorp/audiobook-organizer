- `fragment-consolidation`: chapter files stored one file per folder (`<Author>/<Work> 151 of 341/<file>`) are now grouped by their parent folder. Each group is checked with the same rule as a folder chapter set, and gets one of these classes:
  - **`parent-chapter-set`**: no existing book; one new book.
  - **`parent-set-join-existing`**: the group duplicates an existing book, either by the same title with a total length that agrees, or because one live book already holds its audio (by hash, or by size plus duration). It joins that book the way the existing-book class does: each fragment is retired into it with its listening state and keeps its own file row.
  - **`itunes-chapter-set`**: under iTunes; listed, never applicable.

  The audio join also applies to folder chapter sets. Guards against one book being split across two, or two books being merged into one:
  - files by two different known authors are held (`skipped_mixed_authors`).
  - two files at the same chapter position with different audio are held, as no-parent rows already were.
  - two sets that would each become a new book with the same title are both held (`skipped_same_title_other_set`).
  - a library or import root never forms a parent set.
- `fragment-consolidation`: the audio join for chapter sets has three new conditions.
  - One book must hold the audio of every file in the set. Before, half was enough, so a partial match would have retired fragments whose audio that book lacks, leaving that audio in no live book.
  - The two authors must agree, or one side must have no known author.
  - The evidence now says it is an audio join: how many files matched, both titles, both totals and both authors. It no longer claims the title matches.
