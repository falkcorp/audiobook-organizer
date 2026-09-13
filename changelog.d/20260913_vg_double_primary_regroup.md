### Fixed

- Regroup version-group apply no longer leaves a version group with two primaries when a
  pre-existing member's `is_primary_version` flag is unset. The store reads an unset flag as
  primary, so that member used to list alongside the newly chosen primary; it is now
  written as an explicit false, matching the merge fix (VG-DOUBLE-PRIMARY, #2668).

### Added

- `maintenance.version-group-primary-report`: a report-only, manual op that counts version
  groups with more than one effective primary (split into explicit-true doubles and doubles
  caused only by an unset flag) and groups with no primary, and logs up to 200 double
  groups with each member's stored flag. It sizes the remaining VG-DOUBLE-PRIMARY repair and
  changes nothing.
