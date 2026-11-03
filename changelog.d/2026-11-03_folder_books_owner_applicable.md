### Added

- The folder-books repair can now be applied by the owner for a folder-book
  whose files sit on two shelves (the iTunes folder and the library folder),
  where every hold on the row is one the owner can decide. The row is still
  listed as skipped, and a normal apply, a selection or "apply all" never
  writes it. The owner's own Apply (owner) button creates the missing books,
  retires the folder-book, moves no file and is recorded in the operation, so
  it reverts from the operation like any other apply. New books under the
  library folder start as organized; books on the iTunes shelf keep the
  folder-book's state.
- The folder-books plan counts the single-shelf rows that are held back only by
  checks the owner could resolve, as `single_shelf_owner_resolvable`. It shows
  as a filter chip in the Repairs panel and is written to the plan's log. These
  rows are counted, not offered.
- The owner-apply confirm in the Repairs panel now quotes the row's own
  explanation of what the click writes, so it reads correctly for every repair
  that offers an owner apply.

### Fixed

- A folder-book's files on a second shelf are now grouped under that shelf's own
  author folder when new books are planned, so a plan and the re-plan done at
  apply time agree about which new books belong to the row.
