### Fixed

- `POST /operations/elect-missing-primaries` no longer crowns the earliest-created member of a version group. In the common organize-collision pair that was the `organized_source` copy, which ABS does not list, so the repair kept books hidden. It now uses one shared rule (`internal/versionprimary`): only a live, `organized` copy whose active files are all present under the library root can be primary, ranked m4b with chapters, then m4b without chapters, then metadata, then other formats. Groups with no such copy, or whose better copy is only outside the library, are held and reported instead of written.

### Added

- `maintenance.version-group-primary-repair`: repairs version groups with no primary or more than one using the same rule. Dry run by default, with a per-group report (member states, content tier and chapter source, metadata score, files present, the decision, and the fields that would be filled or conflict). Apply needs explicit `group_ids`, refuses while `library.scan` runs, fills only the winner's empty fields from the best-metadata copy, sets every other live member to explicit false, and records metadata history after each write.
