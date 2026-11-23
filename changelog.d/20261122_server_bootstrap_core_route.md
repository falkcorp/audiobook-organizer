### Fixed

#### `server-bootstrap` skill works before and after the F4-08 identity flip

After the flip, `POST /api/v1/auth/bootstrap` always answers `401 invalid bootstrap token`, and the old `grep -o "token_file=[^ ]*" | tail -1` would have picked up the startup read-only key's `token_file=` line and posted a read-only API key as a bootstrap token. The skill, `scripts/bootstrap.sh` and `references/bootstrap-api.md` now detect the mode from the journal since the restart: `auth: bootstrap token written` means identity (read `<state dir>/.bootstrap-token`, state dir from the unit's `ABO_CORE_PATHS_STATE_DIR`, default `/var/lib/audiobook-organizer`; redeem at `POST /api/core/v1/auth/bootstrap`, key at `.key`), `Emergency access token written` means HEAD (path from that line's `token_file=`; redeem at `POST /api/v1/auth/bootstrap`, key at `.data.api_key`), and neither is an error. `bootstrap.sh` also now writes `.claude/.api-token` as documented instead of `./.api-token`.
