### Changed

- The system-log journal prune (`core.log.journal.prune`) keeps HEAD's log-purge schedule after the cutover. Its default (`core.log.schedules.journal_prune`) is every 168 hours plus a run in the nightly maintenance window at HEAD's position. Before this it was every 24 hours with no window. A new `core.log.journal.prune_in_window` switch (on by default; M2 carries HEAD's `maintenance.purge_old_logs` into it) turns the window run off. A retention of 0 days stops both runs: the clock freezes, and the first check after retention is set again runs the prune once.
- `TestJournalPruneParity_HeadGolden` (books) records what HEAD's scheduler does for the purge across six config rows, and `TestJournalPruneParity` (core/kernel) replays that record against the real prune definition and the kernel default.

### Removed

- `modules.books.maintenance.purge_old_logs`: HEAD's log-purge window toggle now carries into the core key above, and nothing read the books key.
