### Changed

- `maintenance.author-dedup-scan` is retired into `dedup.author-scan` (04-P14a). The old id still resolves as an alias, so a stored row or script that names it runs the survivor, which requires `library.edit_metadata` and has a 1h timeout. The retired copy computed duplicate groups and discarded them, so nothing was ported. The nightly `dedup_refresh` task already enqueued the survivor.
