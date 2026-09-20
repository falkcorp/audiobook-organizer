### Added

- **`maintenance.repoint-version-primary` — move the primary flag onto the
  imported chapter copy so a chapter run can be consolidated.** Chapter
  consolidation refuses a run whose members are all non-primary versions and
  tells the operator to "review the primary copies instead". Measured on
  production 2026-09-19: 226 of the 240 primary directories the blocker names
  hold exactly ONE chapter file, so those primaries can never reach
  `min_files: 2` and the review the message asks for cannot be produced. 84
  blocked groups carry the blocker; on 26 groups / 1,426 books it is the only
  thing keeping them out.

  Each imported per-chapter record sits in a 2-member version group with an
  organized twin that holds the flag. This op moves the flag to the imported
  side and **leaves the version link alone** — the link is the only record that
  the two sides are copies of each other, and dissolving it was considered and
  rejected. The organized single-chapter files stay on disk and in the database;
  what becomes of them is a separate decision.

  "Member of a chapter run" comes from the same detection the consolidator uses,
  not a parallel heuristic: the job calls the shared
  `detectChapterGroupsForRunWithBooks` and reasons over the very snapshot
  detection ran on, so a pair cannot qualify against rows detection never saw. A
  group qualifies on its ROWS — one blocker and every member non-primary — never
  on the blocker's prose, which has already been reworded twice.

  Report-only by default; `{"apply": true, "dry_run": false}` writes, and
  `group_ids` restricts a run to named version groups. Writes go through
  `ModifyBook` with the precondition re-checked on the row it re-read under the
  write lock, so a row that changed underneath is skipped and reported rather
  than clobbered. The flag is promoted first and demoted second, because a
  half-written pair that is briefly double-primary stays visible while one that
  is briefly zero-primary disappears from every listing; a demote that does not
  land reverts the promotion, and a revert that also fails is reported per pair
  with both book ids for a hand fix. The job joins `library.scan`'s concurrency
  key and additionally refuses to write while a scan is running, because a scan
  reverts `library_state` organized→imported — the very field the predicate
  keys on.
