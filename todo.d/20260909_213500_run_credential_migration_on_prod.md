### Finish the credential relocation on the prod host (needs root, in this order)

PR #3171 pinned the encryption key, `.bootstrap-token` and `.readonly-key` to
`/var/lib/audiobook-organizer`. The code landed; the filesystem work did not, because
it needs root. `scripts/finish_credential_migration.py` does all of it and refuses
anything it cannot justify from the state of the host — but the two steps must happen
in this order, and it will not let them happen in the other one:

- [ ] **Deploy #3171** (`make deploy-debug` from the PRIMARY checkout, only at
      `git rev-list --left-right --count HEAD...origin/main` == `0 0`). Safe with the
      key still at the old path: `InitEncryption` probes its legacy directories, uses
      the key it finds, and logs where to move it. Nothing is at risk in the meantime,
      so there is no reason to try to move the key first.
- [ ] **Then** `sudo python3 scripts/finish_credential_migration.py --apply` on the
      server. Removes the stale `/var/lib/audiobook-organizer/.bootstrap-token` (a
      pre-move leftover that still answers `sudo cat` with a value that expired ten
      minutes after it was written — following the runbook against it yields a
      well-formed token and a bare `401`), and moves the key.
- [ ] **Later, after the next restart for any other reason:**
      `sudo python3 scripts/finish_credential_migration.py --remove-legacy-key --apply`
      to retire the renamed-aside `.encryption_key.migrated-*`. It refuses until the
      service has been observed running, started *after* the rename, with the legacy
      name gone — at which point `guardAgainstKeyRegeneration` would have refused to
      boot had the new location not worked, so a live service is the proof.

Verified read-only against prod on 2026-09-09: the deployed binary predates #3171 and
the running process still resolves its state dir to `<root_dir>/.appdata`, so the
script currently refuses the key move on both counts. That refusal firing is the
expected state until the deploy happens.

Do **not** restart the service to hurry any of this along — a restart resumes the
interrupted library scan. The script never restarts it, deliberately.

Note that `.appdata` is `0700 audiobook:audiobook`, so run the script as root or every
path inside it reads as *unknown* (not absent) and the key move stays blocked.
