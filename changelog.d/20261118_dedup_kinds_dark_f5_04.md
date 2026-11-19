### Added

- The dedup plugin now has 31 native task kinds (`books.dedup.*`) beside its 32 existing operations, sharing each operation's code. They stay dark: nothing registers them until the tasks flip, so behavior is unchanged. `dedup.check-book` is not converted yet (core has no `Requires` field); see `docs/agent-tasks/platform-core/seams.d/F5-04.md`.
- A new `split_book_candidates` resource names the split-book candidate rows for task effects.
