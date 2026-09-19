### Fixed

- ABS offline replay: a progress reset now records the position it discarded, and a replayed session that lands back on it is dropped whatever its timestamps, so a pre-reset backlog replayed hours later can no longer undo the reset. The 4x "physical bound" is retired (kept only for tombstones written before positions were recorded), so seeking ahead right after a reset is accepted at once.
