<!-- file: docs/system/deploy-and-gpu-ops.md -->
<!-- version: 1.1.0 -->
<!-- guid: d5e7f9a1-b3c5-4d7e-9f1a-3b5c7d9e1f3a -->
<!-- last-edited: 2026-07-17 -->

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
   make rollback DEPLOY_HOST=192.168.0.10 DEPLOY_BIN=/usr/local/bin/audiobook-organizer
   ```
   (`DEPLOY_HOST`/`DEPLOY_BIN` are normally already set in your
   `Makefile.local`, so you can usually just run `make rollback`.)
3. `rollback` first checks that `$(DEPLOY_BIN).prev` exists on the server
   (errors out cleanly if not — e.g. right after a fresh install with no
   prior deploy), then copies the *currently installed* (bad) binary to
   `$(DEPLOY_BIN).rolled-back` for forensics, restores `.prev` back to
   `$(DEPLOY_BIN)`, and restarts `audiobook-organizer.service`.
4. Dry-run without touching any host:
   ```bash
   make -n rollback DEPLOY_HOST=test-host DEPLOY_BIN=/tmp/x
   ```

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
