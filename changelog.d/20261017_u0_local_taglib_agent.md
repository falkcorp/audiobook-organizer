CI: `test-native-taglib` now runs on any `taglib=true` agent (U1 or the new
local-backend `u0-local` agent on the prod host) instead of U1 only, so it no
longer waits in the queue while U1 is outside its CI window or scaled down.
`scripts/ci/cache_env.sh` finds u0-local's `/srv/ci/cache`, and
`docs/ci/woodpecker.md` §4 documents the agent.
