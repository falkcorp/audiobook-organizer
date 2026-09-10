### Added

- **`scripts/finish_credential_migration.py`** — completes the credential
  relocation on a deployed host: removes stale `.bootstrap-token` files, moves
  `.encryption_key` to the fixed state directory, and reports on `.readonly-key`.

  The three tasks need root, and one of them is destructive **in the wrong
  order**: a pre-#3171 binary looks for the key only beside the database, so
  moving the key before deploying means the next restart generates a fresh one
  and `LoadConfigFromDatabase` deletes every secret it cannot recover from the
  config file. The safe order is deploy first, move second — and deploying with
  the key still at the old path is safe, because `InitEncryption` reads its
  legacy locations and uses what it finds. The script says so in its own output,
  so nobody sequences it backwards out of caution.

  The key move is gated on **behavioural** proof rather than on `git log`: the
  currently running invocation must be observed writing its bootstrap token into
  the new directory. A merged-and-green branch that was never deployed does not
  unlock it, and neither does the marker string being present in the binary on
  disk — that is a cheap pre-check only, since it proves the string is linked in,
  not that the running process resolves paths with it. The destination is then
  read from that same observation instead of hardcoded, so the script cannot
  become a fourth independent derivation of the credential directory in a change
  whose whole point is that three of them diverged.

  The key is **renamed** aside rather than deleted, since it is the one file here
  that cannot be regenerated and a leftover copy is inert once the new path
  works. A separate `--remove-legacy-key` retires it, gated on proof that the app
  has since started from the new location — the service being up, having started
  after the rename, with the legacy name gone, is itself the proof, because the
  new startup guard would have refused to boot otherwise.

  The script never restarts the service: a restart resumes the interrupted
  library scan.

### Fixed

- **File-presence checks in operational scripts no longer read "permission
  denied" as "does not exist".** `Path.exists()` returns `False` on `EACCES`, and
  the production app-data directory is mode `0700`, so a non-root run reported
  the settings encryption key as absent from *both* locations and printed a
  confident note about a directory it had never been able to open. Every
  filesystem question in the migration script is now tri-state — present, absent,
  or unknown — and an unknown blocks the action instead of licensing it.

- **Journal reads in operational scripts survive undecodable bytes and filter
  server-side.** Reading the unit's log with `text=True` raised
  `UnicodeDecodeError` from inside `communicate()` on an invalid continuation byte
  somewhere in 90 days of scanner output, killing a read-only inspection run for
  a reason unrelated to what it was inspecting. Output is now decoded leniently,
  and the query is scoped to the running `_SYSTEMD_INVOCATION_ID` with
  `--grep`, which is both 100x faster (1.2 s against 2 m 27 s, since journalctl no
  longer streams every retained line to the client) and more correct — the live
  token is by definition the one the *running* process wrote, and the last match
  in a wide window can belong to an earlier invocation.
