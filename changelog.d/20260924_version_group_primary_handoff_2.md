### Fixed

#### More paths keep a version group at one primary when they retire, link or crown a member

- Scan: a hash-duplicate linked into a group another writer set up now joins as non-primary, and the group's primary is handed on once the new row exists (including the raced-row branch), instead of possibly getting a second primary or none.
- Transcode: the M4B output gets the original's library state and its own `book_file` row, is created non-primary, and takes the primary only when the original held it and the output is eligible; otherwise `EnsureSinglePrimary` decides. It no longer writes a second primary when the original was not the group's primary, and no longer crowns an output ABS cannot show.
- `CleanupDuplicateVersionGroups`, the broken-segment mark and the no-VG duplicate merge hand the primary on with the shared rule instead of crowning the oldest library copy by hand.
- The iTunes blocked-hash soft-delete, `DeleteAudiobook` (soft and hard), `RestoreAudiobook`, the diagnostics `delete_orphan` suggestion and `CombineBooks` (absorbed shells) hand a retired or restored member's group on.
- `fix-version-groups` hands an unlinked outlier's old group on with the shared rule instead of promoting the lowest ID; a group with no eligible member is held and nobody is promoted.
- The regroup version-group apply picks the primary with the shared rule over the whole group and keeps a healthy incumbent (a member joining a reused group arrives non-primary); when the rule holds the group, the first eligible hold member is crowned, else the earliest-created one.
