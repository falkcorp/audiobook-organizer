### Fixed

- `scripts/ci/update_woodpecker_agents.py` now puts an agent's `no_schedule` back to the value it had before the upgrade instead of always unpausing it, so upgrading U1 outside its CI window no longer turns it back on.

### Added

- The upgrade script can upgrade the coordinator Mac's agent (`mac`) with local commands and a detached relaunch through `run-agent.sh`. It is opt-in, not in the default host list.
