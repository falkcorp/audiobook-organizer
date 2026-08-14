### Added

- `version_group_id` is now a real filter field on the audiobooks list
  (`filters=[{"field":"version_group_id","value":"01…"}]`). Previously it was
  not a filter at all: the bare-param guard 400'd it, and before that guard a
  `?version_group_id=X` request silently listed the entire library. Unblocks
  the clickable version-group link in the UI (G111).
