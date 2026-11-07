### Fixed

- The series-prune repair no longer sees a plan and its re-plan disagree for series whose names differ only in case. Duplicate-group members are now ordered by ID, so the fingerprint no longer depends on which read path (memdb or Pebble) answered, and a preview is no longer refused as "changed since preview" for nothing.
