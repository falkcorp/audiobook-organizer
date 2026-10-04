### Added

#### Repairs fixer: combined author credits (`maintenance.repair-combined-author-credits`)

A census on 2026-10-04 found 1,597 author records whose name joins several
authors who each also exist as their own record ("J.N. Chaney, Jonathan P.
Brazee", "Shirtaloon, Travis Deverell"), credited on 3,120 (book, record)
pairs. The new Repairs fixer lists one row per book:

- `duplicate_link`: the separate authors are already credited. The combined
  credit is removed and positions are renumbered. Where two credits share a
  position, the order in the combined name breaks the tie.
- `combined_only` / `partial_link`: the combined credit is replaced in place by
  its authors. Each author is resolved to its existing record, by exact name
  first and then by letters and digits.
- `split_new_authors`: one or more parts have no author record yet. Apply
  creates that author and journals the creation.

When a book's primary author is the combined record, the primary moves to the
first author credit of the result, so it agrees with the organizer, which files
a book under its lowest-position author. A combined credit's slot takes every
part not already placed before it, in the order the combined name gives them.
These are held, not written: names the shared splitter refuses or
would drop a piece of, parts that read as a title or junk, contributor roles,
a doubled name, more than three names, a name that is a book or series title
in the library, ambiguous spellings, user-locked authors, a stale embedded
series when the primary would move, iTunes books, and Doctor Who / Big Finish /
Torchwood. Only author-role credits change. Narrator credits are left alone.
Apply re-checks the credit list and the primary as a compare-and-set and
journals each write, so the operation revert undoes every row.

### Fixed

#### Author-creating paths no longer mint combined author records

The scanner (including the AI-parse gap fill), the metadata-provider apply,
the batch metadata update, the file importer and refetch-missing-authors
previously looked up and created the whole credit string. A credit naming two
people therefore became one author record named after both. All of these now
go through one shared helper, `internal/authorcredit`. It strips bracketed
segments (a series tag, a reader, an edition), splits the rest with the shared
splitter (now in `personname`), and credits every part in order. A credit is
not split when it names a contributor role, when a part is a publisher, a role
credit or a collective credit ("Full Cast"), when the whole credit or any part
is a book title or series name in the library, or when it names more than
three people. When the split is refused, the whole string is looked up as
before, but a credit naming several people is never created as one author.

The iTunes importer and the single-book edit already split the credit. They
now refuse that same combined case instead of creating it.

`maintenance.author-path-link` holds a folder name that names several people
(`suspect_composite_credit`) instead of linking it or creating a record for it.

The file importer used to create an author only when the lookup returned an
error. A plain miss left the imported book with no author. It now resolves or
creates the author.

#### Copied credits keep their positions

The organized copy (`CreateOrganizedVersion`) and both version-split handlers
copied a book's credits without their positions, so every credit landed at
position 0. A later add-only metadata apply then appended a combined record at
position 1. That is the "J. N. Chaney @0, Jonathan P. Brazee @0, combined @1"
shape seen on Mission Creep. Resolving a production-company author now puts
the real author in the company's slot instead of appending it at position 0.

#### purge-empty-authors holds authors that still own a series

`DeleteAuthor` does not touch series rows. Purging an author that is still a
series' `AuthorID` therefore left that series pointing at a deleted id. The
purge now holds those authors (`held-back(series owner)`, with a sample). The
scanner and the metadata apply create series under the book's primary author,
so many of the combined records the new fixer empties are series owners. They
stay until their series is re-pointed.
