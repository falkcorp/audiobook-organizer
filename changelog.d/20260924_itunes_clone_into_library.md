### Added

- New `maintenance.itunes-clone-into-library` op. It gives a held version group whose only organized member lives in `books/itunes/` a library copy that ABS can list.
  - It reflinks the book's files into the library root. Reflink only: there is no copy or hardlink fallback, and the iTunes files are never written.
  - It creates the organized version through `CreateOrganizedVersion`: the iTunes PIDs move to the library rows, the source becomes `organized_source`, and the group gets a primary.
  - A book already half in the library gets its iTunes files cloned into its existing library folder. Any conflict makes that group report-only.
  - It is dry-run by default. Apply and rollback need explicit `group_ids`. It holds the scan stand-down and skips Doctor Who / Big Finish / Torchwood.
  - Each applied group saves a rollback record, and `rollback: true` restores the PIDs, rows, source state and files.
- The library cloner refuses an occupied destination. It also unwinds any landing that the organizer adopted or renamed to `_copyN`, so a clone never takes over another book's file.
