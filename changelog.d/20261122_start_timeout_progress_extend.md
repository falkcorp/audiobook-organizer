### Fixed

- The first boot after the F3-07 flip is no longer killed by systemd's start timeout during the store rewrite (M8): the rewrite's compaction and copy now extend the start timeout as they make progress (each output table, every 16 MiB or 100,000 keys copied, and once before each discrete step), never on a timer, so a hung rewrite is still killed while a live one finishes. The prod-copy rehearsal now gives the container a NOTIFY_SOCKET and fails if READY=1 arrives after the deadline systemd would have enforced.
