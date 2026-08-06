<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-08-deploy-recipe-and-ollama-scripts.md -->
<!-- version: 1.0.0 -->
<!-- guid: e1113173-4516-4cb0-991b-ef5ae2fa78a8 -->
<!-- last-edited: 2026-07-03 -->

# TASK-08 — Commit deploy recipe templates + manage-ollama-windows.py + rollback target (OPS-1, OPS-2, OPS-6)

**Priority:** P0 · **Effort:** M · **Recommended subagent:** Sonnet · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-08-deploy-recipe-and-ollama-scripts" -b agent/cr-08-deploy-recipe-and-ollama-scripts origin/main
cd "$REPO/.worktrees/cr-08-deploy-recipe-and-ollama-scripts"
git rebase origin/main
```

## Goal

Close OPS-1 (single-machine deploy recipe, no rollback), OPS-2 (Windows GPU box
kept alive by a scheduled task whose setup scripts exist only in a scratchpad),
and the deploy/Ollama-script portions of OPS-6 (operational knowledge landing
outside git). Concretely:

1. Commit **sanitized templates** for the two gitignored local-only files that
   currently hold the entire prod deploy recipe: `Makefile.local.example` and
   `deploy/local.conf.example`.
2. Add a `make rollback` target to the **committed** `Makefile` that restores a
   previously-deployed binary and restarts the systemd unit, plus document (in
   `docs/system/`) the one-line change the operator must make to their own
   real (gitignored) `Makefile.local` `deploy` target so it actually preserves
   a `.prev` copy before overwriting.
3. Recreate the Windows Ollama keepalive — including registering the
   **"OllamaServe" scheduled task** itself, not just the install/pull steps —
   as `scripts/manage-ollama-windows.py` (Python — this repo's rule for
   anything non-trivial; see `CLAUDE.md` "Critical Rules" §4), driven over
   `ssh windows-gpu` using `-EncodedCommand` (base64 UTF-16LE), since a scp'd
   `.ps1` misparses over that SSH path (see status doc, cited below). The
   scheduled task is the actual keepalive mechanism OPS-2 flags as
   undocumented; a script that only installs Ollama and pulls models without
   registering the task leaves that gap open.
4. Document all of the above in `docs/system/`.

Do **not** touch the real (gitignored, machine-local) `Makefile.local` or
`deploy/local.conf` files themselves — they are not in git and this task must
be reproducible by an agent working in a fresh worktree that does not have
them.

## Background (verify before editing)

- **OPS-1** (`docs/consultancy/06-process-and-security.md`, "OPS-1 — Deploy
  recipe is single-machine and has no rollback"): `make deploy`
  (`Makefile.local:13-21` on the author's laptop) cross-compiles, `scp`s the
  binary, then `sudo mv` overwrites `/usr/local/bin/audiobook-organizer` and
  restarts systemd. No copy of the previous binary is kept; rollback today
  means rebuilding an older commit locally. `Makefile.local` and
  `deploy/local.conf` are both gitignored (`.gitignore:7-8` as of this
  writing — re-verify, see grep below) — the entire prod deploy recipe (TLS
  paths, DB path, `WHISPER_REMOTE_URL`, `TimeoutStopSec`) exists only on one
  machine.
- **OPS-2** (same doc, "OPS-2 — Windows GPU box is a triple
  single-point-of-failure held up by an interactive-session scheduled task"):
  `<gpu-host>` serves Whisper transcription, bge-m3 embeddings, and the
  qwen2.5 LLM. Ollama survives only via scheduled task **"OllamaServe"** bound
  to an interactive session (GPU access requires it) — logoff/reboot/Windows
  Update silently kills embeddings + LLM. The setup/start scripts exist **only
  in a session scratchpad**, not in git.
- **OPS-6** (same doc, "OPS-6 — Recurring pattern: operational knowledge lands
  outside git"): names this exact case — `Makefile.local`'s header literally
  says "Local-only targets — not committed", and the Ollama scripts are
  scratchpad-only. The fix pattern: anything referenced by prod must exist in
  git, with only secrets externalized.
- **Status doc** `docs/status/2026-07-02-local-cutover-and-matching.md:30-38`
  ("Local backend setup (Windows GPU box, <gpu-host>)"): reached via
  `ssh windows-gpu` (key preinstalled; alias defined in the operator's
  `~/.ssh/config`, not in this repo). PowerShell over that SSH path
  mis-parses scp'd `.ps1` — use `-EncodedCommand` (base64 UTF-16LE). Ollama is
  kept alive by scheduled task "OllamaServe" (interactive session, because
  `ollama serve` over plain SSH dies on disconnect / doesn't survive headless
  auto-start). Models pulled: `bge-m3` (1024-dim embeddings),
  `qwen2.5:7b-instruct` (LLM). Setup scripts named there:
  `setup-ollama.ps1`, `start-ollama.ps1` — explicit TODO to commit as
  `scripts/manage-ollama-windows.py` (status doc "Pending" item 3, and
  "Local backend setup" bullet 4).

- **Content of the actual (gitignored) files, for reference when writing the
  sanitized `.example` templates** — read them directly if you have access to
  the primary checkout (they will NOT exist in this worktree, since they are
  gitignored and machine-local):
  ```bash
  cat "$REPO/Makefile.local" 2>/dev/null      # may not exist in your environment; that's fine
  cat "$REPO/deploy/local.conf" 2>/dev/null
  ```
  If neither file is available to you, build the templates from the
  citations above and the `Makefile`'s own `backup` target (see step 2 below)
  — do not invent fields not implied by those sources. Redact real hostnames
  down to what already appears in `docs/system/runbooks.md` (`<server>`)
  and `docs/status/2026-07-02-local-cutover-and-matching.md` (`<gpu-host>`);
  do **not** include any username, path under a real home directory, or token
  that isn't already public in those two committed docs. Using these two IPs
  in the `.example` templates and in `scripts/manage-ollama-windows.py` is
  licensed specifically because both already appear, unredacted, in the two
  committed docs cited above (`runbooks.md` for `<server>`, the status doc
  for `<gpu-host>`) — this is not a new PII disclosure and does not reopen
  the SEC-3 "internal infrastructure PII" finding, which concerns *other*
  identifiers not already committed elsewhere.

- **Re-verify these anchors before editing** — content may have drifted since
  this brief was written:
  ```bash
  grep -n "Makefile.local\|local.conf" .gitignore
  grep -n "^DEPLOY_HOST\|^BACKUP_DIR\|-include Makefile.local\|^backup:" Makefile
  sed -n '1,40p' docs/system/runbooks.md
  ls docs/system/
  grep -rn "windows-gpu\|EncodedCommand" scripts/ docs/ 2>/dev/null
  ```
  Confirm the `Makefile` already defines `DEPLOY_HOST ?=` and `BACKUP_DIR ?=`
  as overridable variables and does `-include Makefile.local` (so a
  `rollback` target added to the committed `Makefile` can rely on
  `DEPLOY_HOST`/`DEPLOY_BIN` being supplied by the operator's real
  `Makefile.local`, exactly like the existing `backup` target does). Confirm
  the existing `backup` target's guard-clause pattern
  (`@[ -n "$(DEPLOY_HOST)" ] || (echo "ERROR: ..."; exit 1)`) — reuse it
  verbatim style for `rollback`.

## Step-by-step

1. **`deploy/local.conf.example`** (new file): a sanitized copy of the real
   `deploy/local.conf` systemd drop-in, based on the citations above. Keep the
   structural shape (comment header explaining it's a *template*, `[Service]`
   section, `TimeoutStopSec=`, `Environment=WHISPER_REMOTE_URL=...`, the
   `ExecStart=` clear-and-reset pair with `serve --host --port --db
   --tls-cert --tls-key --http3-port` flags) but mark clearly at the top that
   real secrets/paths (TLS cert paths, actual DB path) must be filled in by
   the operator, and that `WHISPER_REMOTE_URL` should point at their own
   Windows GPU box. Use the `<server>` / `<gpu-host>` hosts since both
   already appear in committed docs — no new PII.

2. **`Makefile.local.example`** (new file, repo root): a sanitized template
   covering the `deploy` and `deploy-debug` targets' *shape* — `DEPLOY_HOST`,
   `DEPLOY_BIN` variables, a `deploy:` target that builds, scp's the binary +
   service files, and restarts via `ssh $(DEPLOY_HOST) 'sudo mv ... &&
   sudo systemctl restart ...'` — plus a comment block at the top explaining
   this is a template (fill in your own `DEPLOY_HOST`), and add the `.prev`
   preservation step described in the next bullet directly into the example's
   `deploy:` recipe so new operators get rollback-safety by default:
   ```
   ssh $(DEPLOY_HOST) 'sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).prev 2>/dev/null; \
     sudo mv /home/USER/audiobook-organizer $(DEPLOY_BIN) && ...'
   ```
   (Adjust to match the real `deploy` target's structure if you were able to
   read it in step 0 of Background; otherwise use the `backup` target's SSH
   idiom as your style guide.)

3. **`Makefile` — add a `rollback` target** (near the existing `backup`
   target, same file, same section):
   ```makefile
   ## rollback: Swap in the previous deployed binary and restart (requires DEPLOY_HOST, DEPLOY_BIN)
   .PHONY: rollback
   rollback:
   	@[ -n "$(DEPLOY_HOST)" ] || (echo "ERROR: DEPLOY_HOST is not set. Add it to Makefile.local or export it."; exit 1)
   	@[ -n "$(DEPLOY_BIN)" ] || (echo "ERROR: DEPLOY_BIN is not set. Add it to Makefile.local or export it."; exit 1)
   	@echo "→ Rolling back $(DEPLOY_BIN) on $(DEPLOY_HOST)..."
   	@ssh $(DEPLOY_HOST) 'test -f $(DEPLOY_BIN).prev' || (echo "ERROR: no $(DEPLOY_BIN).prev found on $(DEPLOY_HOST) — nothing to roll back to."; exit 1)
   	ssh $(DEPLOY_HOST) 'sudo cp $(DEPLOY_BIN) $(DEPLOY_BIN).rolled-back && \
   	  sudo cp $(DEPLOY_BIN).prev $(DEPLOY_BIN) && \
   	  sudo systemctl restart audiobook-organizer.service'
   	@echo "✅ Rolled back $(DEPLOY_BIN) to previous version and restarted."
   ```
   Add `DEPLOY_BIN ?=` next to the existing `DEPLOY_HOST ?=` /
   `BACKUP_DIR ?=` overridable-variable block near the top of `Makefile`
   (re-verify exact line with the grep above), and add `rollback` to the
   `.PHONY` list alongside `backup`. This target is intentionally decoupled
   from the (gitignored) `deploy` target — it only assumes the server-side
   convention `$(DEPLOY_BIN).prev` exists, which `Makefile.local.example`'s
   `deploy:` recipe now creates.
   Bump the `Makefile` file header (version + `last-edited`).

4. **`scripts/manage-ollama-windows.py`** (new file): recreate the Windows
   Ollama keepalive as a single Python CLI, following the
   header/docstring/argparse style of `scripts/manage-whisper-server.py`
   (read it first for tone — shebang-less `uv run` header, `# ///script`
   deps block, module docstring with Usage examples). Requirements, all
   driven over the `windows-gpu` SSH alias (per the status doc — this is
   `ssh`-based, NOT WinRM like `manage-whisper-server.py`; do not copy its
   WinRM transport):
   - `--status` — probe `http://<gpu-host>:11434/api/tags` (or run
     `Invoke-RestMethod` remotely) and report which models are loaded;
     confirm `bge-m3` and `qwen2.5:7b-instruct` are present.
   - `--setup` — idempotent one-time setup: install Ollama via `winget` if
     missing, set `OLLAMA_HOST=0.0.0.0:11434` (User + Machine scope, best
     effort), open firewall port `11434`, `ollama pull bge-m3`,
     `ollama pull qwen2.5:7b-instruct`, **and register the "OllamaServe"
     scheduled task** (see below — this is the part that actually closes
     OPS-2; installing Ollama without capturing the keepalive task leaves
     the reproducibility gap open).
   - `--restart` — kill any `ollama`/`ollama app`/`ollama_llama_server`
     processes, relaunch `ollama serve` bound to `0.0.0.0:11434`, verify via
     `/api/version`.
   - `--install-task` (also callable standalone, and invoked by `--setup`) —
     register a Windows Scheduled Task named exactly `OllamaServe` that runs
     the start/relaunch logic. **Bind it to an interactive logon session
     (`schtasks /create /tn OllamaServe /sc onlogon ...` or the
     `Register-ScheduledTask` equivalent with `New-ScheduledTaskPrincipal
     -LogonType Interactive`), matching the status doc's documented
     constraint that Ollama needs GPU access only available in an
     interactive session — plain `ollama serve` over SSH or a headless
     service dies/loses GPU access.** Do NOT switch this to "run whether
     user is logged on or not" (that plausibly breaks the GPU access the
     interactive binding exists for) — that is a separate hardening step
     OPS-2 flags as future work, not something to silently fold in here.
     Document the current single-point-of-failure (logoff/reboot/Windows
     Update kills it) in the docstring and in `docs/system/`, do not try to
     fix it in this task.
   - Implementation: build one PowerShell script string per subcommand and
     base64-encode it as **UTF-16LE**
     (`base64.b64encode(script.encode("utf-16-le"))`), then invoke via:
     ```python
     subprocess.run(
         ["ssh", "windows-gpu", "powershell", "-NoProfile",
          "-EncodedCommand", encoded],
         capture_output=True, text=True, check=False,
     )
     ```
     Print stdout/stderr, exit non-zero on remote failure.

     Use the following verified PowerShell logic (captured from the actual
     working scripts) as the basis for `--setup`/`--install-task` and
     `--restart` — translate directly, do not re-derive from scratch:

     **Setup / install-task body** (installs Ollama, sets `OLLAMA_HOST`,
     opens the firewall, and — new for this task — registers the
     `OllamaServe` scheduled task bound to interactive logon so that the
     relaunch logic below runs automatically on session start):
     ```powershell
     $ErrorActionPreference = 'Continue'

     Write-Host "=== 1. Ollama install check ==="
     $exe = Get-Command ollama -ErrorAction SilentlyContinue
     if (-not $exe) {
         Write-Host "installing Ollama via winget..."
         winget install --id Ollama.Ollama -e --silent --accept-source-agreements --accept-package-agreements
         $env:Path += ";$env:LOCALAPPDATA\Programs\Ollama"
         $exe = Get-Command ollama -ErrorAction SilentlyContinue
     }
     if ($exe) { Write-Host "ollama: $($exe.Source)" } else { Write-Host "ollama: STILL NOT FOUND (winget may have failed)" }

     Write-Host "=== 2. OLLAMA_HOST=0.0.0.0:11434 (User + Machine) ==="
     [Environment]::SetEnvironmentVariable('OLLAMA_HOST','0.0.0.0:11434','User')
     try { [Environment]::SetEnvironmentVariable('OLLAMA_HOST','0.0.0.0:11434','Machine'); Write-Host "set Machine scope" } catch { Write-Host "Machine scope failed (needs admin); User scope set" }

     Write-Host "=== 3. Firewall rule for 11434 ==="
     try {
         if (-not (Get-NetFirewallRule -DisplayName 'Ollama 11434' -ErrorAction SilentlyContinue)) {
             New-NetFirewallRule -DisplayName 'Ollama 11434' -Direction Inbound -LocalPort 11434 -Protocol TCP -Action Allow -ErrorAction Stop | Out-Null
             Write-Host "firewall rule added"
         } else { Write-Host "firewall rule already present" }
     } catch { Write-Host "firewall rule failed (needs admin): $($_.Exception.Message)" }

     Write-Host "=== 4. Register OllamaServe scheduled task (interactive logon, current user) ==="
     $scriptPath = "$env:USERPROFILE\ollama-start.ps1"   # written by --setup alongside this task
     $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -WindowStyle Hidden -File `"$scriptPath`""
     $trigger = New-ScheduledTaskTrigger -AtLogOn
     $principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
     $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
     try {
         Unregister-ScheduledTask -TaskName 'OllamaServe' -Confirm:$false -ErrorAction SilentlyContinue
         Register-ScheduledTask -TaskName 'OllamaServe' -Action $action -Trigger $trigger -Principal $principal -Settings $settings | Out-Null
         Write-Host "OllamaServe scheduled task registered (AtLogOn, interactive)"
     } catch { Write-Host "scheduled task registration failed: $($_.Exception.Message)" }

     Write-Host "=== 5. pull models ==="
     & ollama pull bge-m3
     & ollama pull qwen2.5:7b-instruct

     Write-Host "=== 6. status ==="
     try {
         $t = Invoke-RestMethod -Uri 'http://127.0.0.1:11434/api/tags' -TimeoutSec 6
         Write-Host ("api up. models: " + (($t.models | ForEach-Object { $_.name }) -join ', '))
     } catch { Write-Host "api 11434 not responding: $($_.Exception.Message)" }
     ```
     Note: the task action above launches
     `%USERPROFILE%\ollama-start.ps1` — have `--setup` also write that file
     to the remote box (base64-encode it and `[System.IO.File]::WriteAllBytes`,
     same pattern `manage-whisper-server.py`'s `cmd_deploy` already uses for
     `whisper_server.py`) using this **relaunch body** for `--restart` /
     the task's own trigger action:
     ```powershell
     $ErrorActionPreference = 'Continue'
     $ProgressPreference = 'SilentlyContinue'
     $exe = Join-Path $env:LOCALAPPDATA 'Programs\Ollama\ollama.exe'

     Get-Process ollama,'ollama app' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
     Start-Sleep -Seconds 2

     $psi = New-Object System.Diagnostics.ProcessStartInfo
     $psi.FileName = $exe
     $psi.Arguments = 'serve'
     $psi.EnvironmentVariables['OLLAMA_HOST'] = '0.0.0.0:11434'
     $psi.UseShellExecute = $false
     $psi.RedirectStandardError = $true
     $psi.RedirectStandardOutput = $true
     $p = [System.Diagnostics.Process]::Start($psi)
     Write-Host "started ollama serve pid=$($p.Id)"
     Start-Sleep -Seconds 8

     Write-Host "=== netstat 11434 ==="
     netstat -ano | Select-String ':11434' | ForEach-Object { $_.Line }

     Write-Host "=== local api probe ==="
     try { $t = Invoke-RestMethod 'http://127.0.0.1:11434/api/version' -TimeoutSec 5; Write-Host "api version: $($t.version)" } catch { Write-Host "api down: $($_.Exception.Message)" }
     ```
   - Document in the module docstring that this depends on the operator
     having a `windows-gpu` `Host` alias in their own `~/.ssh/config`
     (not committed — machine-local, same as the existing
     `scripts/setup-ssh-from-mac.sh` which documents how to create it).
   - Add the standard file header.

5. **`docs/system/` documentation** — add a new "Deploy Rollback & Windows
   GPU Keepalive" section (either a new file `docs/system/deploy-and-gpu-ops.md`
   or appended to `docs/system/runbooks.md` under a new `##` heading — prefer
   a new file if `runbooks.md`'s existing sections are already dense; check
   its current length first). Cover:
   - How to instantiate `Makefile.local.example` → `Makefile.local` and
     `deploy/local.conf.example` → `deploy/local.conf` on a fresh operator
     machine.
   - The rollback flow: `make deploy` (operator's real target, once updated
     per step 2) creates `.prev`; `make rollback` swaps back; note the
     `.rolled-back` copy `rollback` leaves behind for forensics.
   - How to run `scripts/manage-ollama-windows.py --status` /
     `--setup` / `--install-task` / `--restart`, and the OPS-2 residual risk
     this does NOT yet close: the `OllamaServe` task is still bound to an
     interactive logon session (required for GPU access — do not "fix" this
     by switching to a headless/service run, per the step-4 warning) and a
     logoff/reboot/Windows Update can still kill it before the next logon;
     a periodic reachability probe via `/metrics` is explicitly out of scope
     for this task — leave as a follow-up note, do not build it here.
   - Cross-reference `docs/status/2026-07-02-local-cutover-and-matching.md`
     and `docs/consultancy/06-process-and-security.md` (OPS-1/OPS-2/OPS-6).

6. Bump file headers on every file you touch/create (per
   `.standards/instructions/file-headers.md`): `Makefile`,
   `Makefile.local.example`, `deploy/local.conf.example`,
   `scripts/manage-ollama-windows.py`, and the new/edited `docs/system/` file.

## How to test

This is a docs/scripts/Makefile task — no Go package changes are expected,
but run the full gate anyway to catch accidental breakage (e.g. a `.PHONY`
typo or Makefile syntax error breaking other targets):

```bash
go build ./...
go vet ./...
make -n rollback DEPLOY_HOST=test-host DEPLOY_BIN=/usr/local/bin/audiobook-organizer   # dry-run: prints the commands, does not execute
python3 -m py_compile scripts/manage-ollama-windows.py
```

`make -n rollback ...` must print the guarded `ssh` commands without erroring
(the `-n` flag makes `make` show what it would run without executing it —
do NOT drop `-n` here, this must never actually touch a real host during
review). Do not run `scripts/manage-ollama-windows.py` against the real
`windows-gpu` host as part of this task's verification — a syntax/compile
check (`py_compile`) is sufficient; live execution is the operator's call
once merged.

## Acceptance criteria

- [ ] `deploy/local.conf.example` exists, is a generic template (no real
      secrets/paths beyond what's already public in committed docs), and has
      a file header.
- [ ] `Makefile.local.example` exists at repo root, is a generic template
      including a `.prev`-preserving `deploy:` recipe, and has a file header.
- [ ] `Makefile` has a new `rollback` target next to `backup`, guarded on
      `DEPLOY_HOST` and `DEPLOY_BIN` being set, added to `.PHONY`, and
      `make -n rollback DEPLOY_HOST=test-host DEPLOY_BIN=/tmp/x` prints the
      expected commands without error.
- [ ] `scripts/manage-ollama-windows.py` exists, implements `--status`,
      `--setup`, `--install-task`, `--restart` over
      `ssh windows-gpu ... -EncodedCommand` (UTF-16LE base64), registers the
      `OllamaServe` scheduled task bound to interactive logon (not headless),
      has the standard file header, and passes `python3 -m py_compile`.
- [ ] A new or extended file under `docs/system/` documents the rollback flow
      and the Ollama script usage, cross-referencing the status doc and
      OPS-1/OPS-2/OPS-6.
- [ ] `go build ./...` and `go vet ./...` remain green (no Go files should
      need changes for this task; if they do, something is out of scope —
      stop and reconsider).
- [ ] The real, gitignored `Makefile.local` and `deploy/local.conf` are
      untouched (they should not exist in this worktree at all — if `ls
      Makefile.local deploy/local.conf` finds them, you are not in a clean
      worktree; stop).
- [ ] File headers bumped on every changed/created file.

## Commit message

```
feat(ops): commit sanitized deploy templates, rollback target, and Windows Ollama script (OPS-1/OPS-2/OPS-6)

The entire prod deploy recipe lived only in gitignored Makefile.local +
deploy/local.conf on one laptop, with no rollback path, and the Windows GPU
box's Ollama keepalive (including the "OllamaServe" scheduled task) existed
only as scratchpad scripts. Commit sanitized *.example templates, add a
`make rollback` target that restores the previous deployed binary via a
server-side .prev copy, and recreate the Windows setup/start/status/task
scripts as scripts/manage-ollama-windows.py (ssh windows-gpu +
-EncodedCommand, per the documented PowerShell-over-SSH parsing gotcha),
including registering the OllamaServe scheduled task so the keepalive
mechanism itself is reproducible from git.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-08-deploy-recipe-and-ollama-scripts
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `Makefile.local.example` and `deploy/local.conf.example` already exist,
`scripts/manage-ollama-windows.py` already exists with `--status`/`--setup`/
`--restart` implemented over the `windows-gpu` SSH alias, and `Makefile`
already has a `rollback` target — this task is done. Verify with:

```bash
ls Makefile.local.example deploy/local.conf.example scripts/manage-ollama-windows.py
grep -n "^rollback:\|^\.PHONY.*rollback" Makefile
grep -n "EncodedCommand" scripts/manage-ollama-windows.py
```

If any of these already exist but are incomplete (e.g. the `.example`
templates exist but `rollback` doesn't, or vice versa), only add the missing
piece(s) — do not duplicate or overwrite work that's already landed.

Rollback of this change = revert the commit. Reverting is safe: the new
`rollback` Makefile target and `.example` files are additive (no existing
target, variable, or file is modified in a breaking way — `DEPLOY_HOST ?=`
and `BACKUP_DIR ?=` already existed; only `DEPLOY_BIN ?=` is newly added as
an overridable default, which is backward compatible since the operator's
real `Makefile.local` already sets it directly). The real `Makefile.local`
and `deploy/local.conf` on the operator's laptop are never touched by this
change, so reverting cannot break the live deploy pipeline.
