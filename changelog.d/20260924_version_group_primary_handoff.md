### Fixed

#### Version groups no longer lose or double their primary when a member is retired, moved or edited

- New `versionprimary.EnsureSinglePrimary` / `Crown` hand-off: after a path retires or moves a group's primary, the group is re-checked and, if it has no eligible primary (or more than one), the best live member that ABS can show (organized, files present under the library root) is crowned with the shared election rule and every other live member is set to explicit false. A healthy group keeps its incumbent; a held group (no eligible copy, or a better copy outside the library) is logged and left for `maintenance.version-group-primary-repair`.
- Wired into `dedup.MergeBooks`, the split-book cluster merge, `merge.MergeBooks` (the groups a participant leaves), `purge-unknown-author-duplicates`, `quarantine-chapter-artifacts`, the `fs-regroup-xml` shell retire, and batch update/delete/restore.
- `dedup-books` now elects the successor of a retired primary with the shared rule instead of the earliest-created member, and promotes nobody when the group is held.
- Organize: a new library copy takes the primary when the group's incumbent cannot be shown by ABS, instead of always yielding; a failed version-group read now refuses the organize (rolling back the landed files) instead of possibly creating a second primary.
- Batch `is_primary_version=true` and the fs-regroup undo of a primary demotion now demote the rest of the group instead of writing a second primary.
