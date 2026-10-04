<!-- file: docs/system/deploy-and-gpu-ops.md -->
<!-- version: 1.4.0 -->
<!-- guid: d5e7f9a1-b3c5-4d7e-9f1a-3b5c7d9e1f3a -->
<!-- last-edited: 2026-10-04 -->

# Deploy Rollback & Windows GPU Keepalive

This document closes the operational gaps flagged in
[`docs/consultancy/06-process-and-security.md`](../consultancy/06-process-and-security.md)
as **OPS-1** (single-machine deploy recipe, no rollback), **OPS-2** (Windows
GPU box kept alive by a scheduled task whose setup scripts existed only in a
scratchpad), and **OPS-6** (operational knowledge landing outside git). See
also [`docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md`](../archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md)
for the local-backend cutover this Windows box supports.

## 1. Instantiating the local, gitignored config from the committed templates

Both `Makefile.local` and `deploy/local.conf` are gitignored (they hold
machine-local paths, TLS cert locations, and the real `DEPLOY_HOST`). Two
sanitized templates are committed instead:

```bash
cp Makefile.local.example Makefile.local
$EDITOR Makefile.local          # set DEPLOY_HOST, DEPLOY_BIN, fix the scp source path

cp deploy/local.conf.example deploy/local.conf
$EDITOR deploy/local.conf       # set real TLS cert/key paths, DB path, WHISPER_REMOTE_URL
```

`deploy/local.conf` is deployed as a systemd drop-in at
`/etc/systemd/system/audiobook-organizer.service.d/local.conf` (see
[`runbooks.md`](runbooks.md#systemd-service) for the existing deploy runbook);
`Makefile.local` is picked up automatically by the committed `Makefile` via
`-include Makefile.local`.

## 2. Rollback flow

`Makefile.local.example`'s `deploy:` recipe now preserves the previously
deployed binary before overwriting it:

```
ssh $(DEPLOY_HOST) 'sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).prev 2>/dev/null; \
  sudo mv /home/USER/audiobook-organizer $(DEPLOY_BIN) && ...'
```

Once your real `Makefile.local`'s `deploy` target includes this same
`.prev`-preserving line, the flow is:

1. `make deploy` — builds, ships, and (via the line above) copies the
   currently-running binary to `$(DEPLOY_BIN).prev` on the server before
   installing the new one and restarting the service.
2. If the new deploy is bad, run the committed rollback target:
   ```bash
   make rollback DEPLOY_HOST=192.0.2.10 DEPLOY_BIN=/usr/local/bin/audiobook-organizer DEPLOY_DB=/path/to/audiobooks.pebble
   ```
   (`DEPLOY_HOST`/`DEPLOY_BIN`/`DEPLOY_DB` are normally already set in your
   `Makefile.local`, so you can usually just run `make rollback`. `DEPLOY_DB`
   is the main Pebble store path on the server, the `--db` /
   `DATABASE_PATH` value; `rollback` refuses to run without it.)
3. `rollback` first checks that `$(DEPLOY_BIN).prev` exists on the server
   (errors out cleanly if not — e.g. right after a fresh install with no
   prior deploy).
4. **Storage-format guard.** `rollback` then reads the store's format from
   `$(DEPLOY_DB).storage-format` and asks `$(DEPLOY_BIN).prev
   --print-storage-format` which format it supports (a build older than the
   flag counts as `1`), and runs `scripts/storage_format_guard.py`. If the
   store is above the format `.prev` supports, it refuses, swaps nothing, and
   prints the restore-from-checkpoint steps (see
   [`runbooks.md`](runbooks.md#storage-format-restore)). If the sidecar is
   missing or unreadable it also refuses, and the refusal quotes the stderr of
   the ssh command that tried to read it (for example `sudo: a password is
   required`). `ROLLBACK_IGNORE_FORMAT=1 make rollback` overrides only that
   case, after you have checked the store is at format 1, and even then the
   guard asks the *current* binary for its format (`$(DEPLOY_BIN)
   --print-storage-format`) and refuses if it is above `.prev`'s, or if it
   gives no answer at all (a crash, an ssh failure). Only a printed format, or
   the old-build "unknown flag: --print-storage-format" error, counts. Nothing
   overrides a format change.
5. Only then does it copy the *currently installed* (bad) binary to
   `$(DEPLOY_BIN).rolled-back` for forensics, restore `.prev` back to
   `$(DEPLOY_BIN)`, and restart `audiobook-organizer.service`.
6. Dry-run without touching any host:
   ```bash
   make -n rollback DEPLOY_HOST=test-host DEPLOY_BIN=/tmp/x DEPLOY_DB=/tmp/db
   ```

### sudoers for `make rollback` (owner action)

`make rollback` runs every privileged step over a plain `ssh HOST 'cmd'`,
which has no tty, so `sudo` cannot prompt for a password. The sidecar and the
checkpoint record are read with `sudo -n cat` (fail at once instead of
hanging), and the store's parent directory (`.appdata` on prod) is not
readable without sudo. Without passwordless rules for exactly these commands,
every rollback refuses with "cannot read the store's storage format", and the
swap itself would fail too.

Add a drop-in with `sudo visudo -f /etc/sudoers.d/audiobook-organizer-rollback`
on the deploy host. Replace `deploy` with the ssh user, and the two paths with
the real `DEPLOY_DB` and `DEPLOY_BIN` values (sudoers needs literal paths, so
no variables):

```
deploy ALL=(root) NOPASSWD: /usr/bin/cat /path/to/audiobooks.pebble.storage-format, \
    /usr/bin/cat /path/to/audiobooks.pebble.migration-checkpoint, \
    /usr/bin/cp /usr/local/bin/audiobook-organizer /usr/local/bin/audiobook-organizer.rolled-back, \
    /usr/bin/cp /usr/local/bin/audiobook-organizer.prev /usr/local/bin/audiobook-organizer, \
    /usr/bin/systemctl restart audiobook-organizer.service
```

Check it with `ssh HOST 'sudo -n cat /path/to/audiobooks.pebble.storage-format'`.
It should print one integer and not ask for a password. Use `command -v cat cp
systemctl` on the host if those binaries live somewhere other than `/usr/bin`.

Note this only preserves **one** prior version — a second consecutive `make
deploy` overwrites `.prev` with the (now second-to-last) binary. If you need
deeper history, keep your own timestamped copies (see `make backup` in
`Makefile` for the equivalent pattern applied to data directories).

## 3. Windows GPU Ollama keepalive (`scripts/manage-ollama-windows.py`)

The Windows GPU box (`192.168.0.20`, reached via the `windows-gpu` SSH alias —
see `scripts/setup-ssh-from-mac.sh` for creating that alias) runs Ollama
serving `bge-m3` (1024-dim embeddings) and `qwen2.5:7b-instruct` (LLM).
Commands are sent as base64-encoded (UTF-16LE) PowerShell via
`ssh windows-gpu powershell -NoProfile -EncodedCommand ...`, because a
scp'd `.ps1` mis-parses over that SSH path (documented in the status doc
cited above).

```bash
uv run scripts/manage-ollama-windows.py --status         # report loaded models
uv run scripts/manage-ollama-windows.py --setup           # install, firewall, register task, pull models
uv run scripts/manage-ollama-windows.py --install-task    # register OllamaServe only
uv run scripts/manage-ollama-windows.py --restart         # kill + relaunch ollama serve
```

`--setup` and `--install-task` register a Windows Scheduled Task named
**`OllamaServe`**, bound to an interactive logon session
(`New-ScheduledTaskPrincipal -LogonType Interactive`). This is the actual
keepalive mechanism OPS-2 flagged as undocumented — recreating the
install/pull steps without registering this task would leave the
reproducibility gap open.

### Residual risk (OPS-2 — not closed by this script)

`OllamaServe` is bound to an interactive logon session because `ollama
serve` needs GPU access that is unavailable in a headless/service context —
plain `ollama serve` over SSH or a "run whether user is logged on or not"
service dies or loses GPU access. **Do not** switch the task to headless
mode to "fix" this; that plausibly breaks the GPU access the interactive
binding exists for. Practical consequence: a logoff, reboot, or Windows
Update can still kill the Ollama process before the next interactive logon,
and nothing currently pages an operator when that happens. A periodic
reachability probe (e.g. hitting `/metrics` or `--status` on a schedule) is
explicitly **out of scope** for this task — treat it as a follow-up
hardening item, not something silently folded in here.

## Cross-references

- [`docs/consultancy/06-process-and-security.md`](../consultancy/06-process-and-security.md) — OPS-1, OPS-2, OPS-6 findings this document addresses.
- [`docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md`](../archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md) — origin of the Windows GPU setup and the `-EncodedCommand` gotcha.
- [`runbooks.md`](runbooks.md) — general deploy runbook and systemd service management.
