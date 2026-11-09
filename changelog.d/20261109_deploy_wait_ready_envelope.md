### Fixed

- `scripts/deploy-wait-ready.sh` reads `ready` from the `data` envelope the server wraps `/health` in. R1's rewrite of the script read only a top-level `ready`, so against a real server it exited 2 ("no ready field") on the first answer; the earlier script matched the text `"ready":true` and was not affected.
