### Changed

- A request that still names `maintenance.series-normalize` now runs `dedup.series-normalize`, which also moves the affected books' files and writes their tags (the old op only renamed series rows). An interrupted run of either old ID (`maintenance.series-prune` or `maintenance.series-normalize`) is dropped on restart, following the survivor's resume policy, instead of being requeued. Priority moves from low to normal.
