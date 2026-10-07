### Fixed

- **Version twin fixer: an unknown primary runtime no longer drops narrator
  evidence.** The primary carrying the record's narrator is edition evidence
  unless two known runtimes are more than 1% apart; a primary whose own
  runtime is unknown, with twin and record agreeing, now gets the narrator
  and ASIN again.
- **Version twin fixer: an identifier gained after the re-plan is caught.**
  The outside-group ASIN/ISBN check runs again just before the write, so a
  book outside the group that gained the record's ASIN or ISBN since the
  re-plan refuses the apply.
- **Repairs: "memdb not serving" is reported as `retry_later`, not
  `changed_since_plan`.** A new apply outcome (`repairs.ErrRetryLater`) for a
  row the fixer could not check right now: nothing was written, the row did
  not change, and it stays selectable in the Repairs lane (a
  `changed_since_plan` row is treated as settled and is not re-sent). A
  resumed apply retries it.
- **Version twin fixer: a candidate copy this operation already journaled is
  recognised on a re-run** and is not journaled a second time.

### Added

- Tests for `PebbleStore.GetBooksByMetadataSourceHashInMemory` (warm,
  lost rows, memdb off, unpublished) and for the server's lookup through
  the production `indexedStore` decorator.
