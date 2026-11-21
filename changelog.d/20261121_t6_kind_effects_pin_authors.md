### Fixed

- `TestT6Kinds_EffectsEqualThePinnedSets` was red on main: ten native maintenance kinds (title-backfill, title-repair, bulk-write-back, tag-backfill, repoint-unrecorded-renames, series-phantom-repair, version-group-primary-repair, rewrite-path-prefix, repoint-missing-to-folder-audio, series-denumber) still had `authors` in their hand-typed write pins, left over from before F5-18j took `authors` out of the book write. Their declared effects were already right: none of them calls a method that writes the credit list, so the pins dropped `authors` and the declarations did not change.
