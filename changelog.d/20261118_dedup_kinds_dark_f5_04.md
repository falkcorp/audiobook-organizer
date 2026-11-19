### Added

- The dedup plugin now has 29 native task kinds (`books.dedup.*`) beside its 32 existing operations, sharing each operation's code. They stay dark: nothing registers them until the tasks flip, so behavior is unchanged. `dedup.check-book`, `dedup.split-book-scan` and `dedup.split-book-bulk-merge` are not converted yet (see `docs/agent-tasks/platform-core/seams.d/F5-04.md`).
