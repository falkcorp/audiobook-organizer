### Changed

- `maintenance.authority-build` now follows the dry run, review, apply rule.
  A dry run stores its plan as the op result: a digest, the full list of keys
  it would prune, the rows it holds back, and any reason an apply would be
  refused. `dry_run=false` needs `plan_op_id` and applies exactly that plan.
  It re-derives the plan with the dry run's sources and refuses if the digest
  differs, which happens when the store or the sources changed.
- An authority-list rebuild no longer deletes rows from sources that did not
  run. A row is pruned only when every source it records ran. An apply is
  refused while the seed or catalog is skipped, while owner-export rows exist
  but no export was supplied, or while any payload failed to decode.
- Role-marked credits ("Jane Doe - translator", "- illustrator",
  "- introduction") are split with `metadata.ClassifyContributor`. The bare
  name is kept and the credit is recorded as role `other`, which is never
  author evidence. The embedded seed was regenerated: 10 such entries are gone
  (8 translators, 1 illustrator, 1 introduction), and `also_author`, which was
  never set, was dropped.
- Duplicate product ASINs within one source (the same product in two
  marketplaces) now resolve the same way on every run, whatever the worker
  count.
- The dry-run result (`GET /operations/:id/result`) now reads from the top
  down: a one-line `summary`, `refusals`, the `apply_params` to send, counts,
  then the full `prune` and `held` lists, with the detailed report last.
- The author-catalog harvest scope now skips publisher-shaped names that the
  junk list missed ("Big Finish Production", "... Studio") and role-marked
  credits ("Jay Rubin - translator"). A credit list that contains a
  role-marked piece is never harvested whole. Its unmarked person pieces are
  harvested on their own instead, for example "Haruki Murakami" from
  "Haruki Murakami, Jay Rubin - translator, Philip Gabriel - translator". The
  scope census and the harvest log count each kind of skip.
