### Changed

- CI: the U1 Woodpecker agent now runs around the clock, with 6 parallel workflows inside its daytime windows and 1 outside them, instead of being paused outside those windows. The schedule job drains U1 before each restart, so no running job is killed. `docs/ci/woodpecker.md` describes the schedule.
- CI: the llm1 Woodpecker agent runs 3 parallel workflows (was 2).
