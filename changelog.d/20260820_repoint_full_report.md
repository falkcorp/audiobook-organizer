- **Fixed:** `maintenance.missing-file-repoint` discarded 71,914 of its 71,954 per-row
  decisions. It now writes every one to a TSV (`reports/missing-file-repoint-<opID>.tsv`,
  overridable with `reportPath`) with a `bucket` column, so a dry run can actually be
  reviewed before the apply. `no-shape` and `no-bytes` rows previously produced no
  record at all.
- **Fixed:** the in-log sample kept the first 40 decisions in arrival order, which on the
  first prod run meant 40 collision rows from 3 adjacent books and zero of the 14,439
  rows it would rewrite. It is now stratified per bucket.
- **Fixed:** the collision warning asserted "the flattened-directory shape" as the cause.
  Measured on prod, the colliding rows belong to duplicate book records; the message now
  states what was counted instead of a cause it had not checked.
