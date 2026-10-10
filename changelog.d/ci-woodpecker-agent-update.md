### Added

- `scripts/ci/update_woodpecker_agents.py` upgrades the native Woodpecker agents on U1 and llm1 to the server's version: it pauses each agent, waits for its running tasks, swaps in the checksum-verified release binary, and passes only when the server reports the new version, rolling back otherwise. Both agents moved from 3.18.1 to 3.19.0 with it on 2026-10-10.
