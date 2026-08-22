<!-- file: docs/agent-tasks/todo-completion/docs/TASK-194-harden-the-systemd-unit-protectsystem-strict-rea.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0e033ca6-4603-4c4d-b443-f2711034f829 -->
<!-- last-edited: 2026-08-21 -->

# TASK-194 — Harden the systemd unit: ProtectSystem=strict, ReadWritePaths, CapabilityBoundingSet, SystemCallFilter, and an egress allowlist (TODO-SEC-SYSTEMD)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · docs subagent · **Why:** mechanical systemd directive addition across two duplicate files, but requires care getting CapabilityBoundingSet/SystemCallFilter narrow enough not to break the Go binary's actual syscalls (file I/O, network, subprocess exec for ffmpeg/taglib) -- needs a real prod smoke test, not just a syntax-valid unit file · **Depends on:** none · **External blockers:** TODO.md L10077 (needs_design) — not a task in this package; coordinator confirms it is resolved or explicitly waives it before dispatch · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10094 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**TODO-SEC-SYSTEMD**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-194-harden-the-systemd-unit-protectsystem-strict-rea" -b agent/docs-194-harden-the-systemd-unit-protectsystem-strict-rea origin/main
cd "$REPO/.worktrees/docs-194-harden-the-systemd-unit-protectsystem-strict-rea"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add the five missing systemd hardening directives to BOTH deploy/audiobook-organizer.service and deploy/systemd/audiobook-organizer.service (kept byte-identical today, so the same edit applies to each verbatim): ProtectSystem=strict (with ReadWritePaths explicitly listing the directories the service must write to -- at minimum WorkingDirectory /var/lib/audiobook-organizer, the Pebble DB path, /var/log/audiobook-organizer, and any configured library/import root), CapabilityBoundingSet narrowed to whatever this binary genuinely needs (start from an empty set and add back only what a smoke-test run proves necessary -- likely none beyond defaults for a userspace HTTP server with no raw sockets or privileged ports), SystemCallFilter=@system-service (systemd's broad safe-default group, the conservative starting point before narrowing further), and IPAddressDeny=any paired with an IPAddressAllow allowlist covering: loopback, the Whisper host's port (per the TODO, :19847 -- reconcile against the :8000 shown in deploy/local.conf.example's WHISPER_REMOTE_URL doc placeholder, since the TODO and the example disagree on the port and the real prod value is server-side/gitignored), the Ollama host's port :11434 (matches OLLAMA_BASE_URL's documented placeholder), and outbound HTTPS (443) for the OpenLibrary and AcoustID metadata providers.

## Background (verify before editing)

- The unit already has User=audiobook, NoNewPrivileges=yes, ProtectKernelTunables=yes, ProtectControlGroups=yes, PrivateTmp=yes (all four verified present, identically, in both tracked unit files).
- deploy/local.conf.example documents WHISPER_REMOTE_URL and OLLAMA_BASE_URL as the two external AI-service dependencies this service calls outbound, using a placeholder address (192.168.0.20) explicitly labeled as a stand-in for the reader's own hosts -- the real production values live in the gitignored server-side deploy/local.conf drop-in, which this scout cannot read.
- The two unit files (deploy/audiobook-organizer.service and deploy/systemd/audiobook-organizer.service) are confirmed byte-identical; it is unclear from the repo alone which one is actually installed on the server or whether one is a stale duplicate that should be removed instead of dual-maintained -- flagging for the coordinator rather than guessing.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'User=audiobook\|NoNewPrivileges\|ProtectKernelTunables\|ProtectControlGroups\|PrivateTmp' deploy/systemd/audiobook-organizer.service   # 5 hits, L60/94/95/96/97 (identical in deploy/audiobook-organizer.service) — the directives the TODO says already exist are present in both unit files
  grep -c 'ProtectSystem=strict\|ReadWritePaths\|CapabilityBoundingSet\|SystemCallFilter\|IPAddressDeny' deploy/systemd/audiobook-organizer.service deploy/audiobook-organizer.service   # 0 in both files — the five hardening directives the TODO wants added are currently absent from both unit files
  diff deploy/audiobook-organizer.service deploy/systemd/audiobook-organizer.service   # empty (byte-identical) — the two unit files are exact duplicates and both need the same edit
  grep -n 'WHISPER_REMOTE_URL\|OLLAMA_BASE_URL' deploy/local.conf.example   # 2 hits, L39-40: http://192.168.0.20:8000 (Whisper) and http://192.168.0.20:11434/v1 (Ollama) -- confirms the ports named in the TODO (:19847 Whisper, :11434 Ollama) belong with these two env vars — the Ollama/Whisper endpoints the egress allowlist must not block are configured via env vars this unit already sets, with a documented placeholder IP/port pattern to follow (192.168.0.20 is an explicit doc placeholder, not a real address)
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In deploy/audiobook-organizer.service, locate the existing hardening block (the five directives at lines 94-97 plus surrounding context) inside the [Service] section.
2. Add `ProtectSystem=strict` directly below PrivateTmp=yes.
3. Add `ReadWritePaths=/var/lib/audiobook-organizer /var/log/audiobook-organizer` plus any additional path the deployment's WorkingDirectory/Environment lines reference for the Pebble DB or the audiobook library root (grep the same file for WorkingDirectory= and any AUDIOBOOK_ROOT_DIR/--db path defaults already set, and include those directories too -- ProtectSystem=strict makes the ENTIRE filesystem read-only except paths explicitly listed here, so omitting one breaks the service at write time, not at start time).
4. Add `CapabilityBoundingSet=` listing only capabilities a smoke test proves necessary -- start with an empty CapabilityBoundingSet= (drops everything) and run the service under it in a non-prod environment first; add back specific capabilities (e.g. CAP_NET_BIND_SERVICE only if the configured port is <1024, which 8484 is not, so likely none are needed) only if the smoke test shows a permission failure tied to a specific syscall.
5. Add `SystemCallFilter=@system-service` as the starting allowlist group (systemd's built-in curated safe-default set for ordinary services) rather than hand-picking individual syscalls, to avoid breaking ffmpeg/taglib subprocess execution or file I/O the binary depends on.
6. Add `IPAddressDeny=any` followed by `IPAddressAllow=` entries for: `127.0.0.0/8` and `::1` (loopback, needed for local health checks), the Whisper host's address:port (reconcile the TODO's :19847 against local.conf.example's :8000 placeholder with the owner before committing to one -- do not silently pick either), the Ollama host's address:port (:11434, matching OLLAMA_BASE_URL), and note that IPAddressAllow entries are address/CIDR based, NOT hostname+port based, so OpenLibrary/AcoustID (accessed over HTTPS to their own IP ranges, which vary) may need a broader allow (e.g. `IPAddressAllow=any` scoped only by an explicit `IPAddressDeny=` list of RFC1918 LAN ranges the service should NOT reach, inverting the approach) if a narrow per-provider CIDR allowlist proves impractical to maintain -- flag this specific sub-decision in the PR description rather than silently picking the fragile approach.
7. Apply the identical edit to deploy/systemd/audiobook-organizer.service so the two files stay in sync (or, if the coordinator/owner decides one file is stale, remove it in a follow-up rather than as part of this edit -- out of scope here per Fix It Right's scope-discipline: this item is about hardening directives, not file consolidation).
8. Bump both files' version headers per the repo's file-header convention (grep the top of each file for the existing `# version:` line and increment it).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_194.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- ReadWritePaths omitting the actual Pebble DB directory (if it differs from /var/lib/audiobook-organizer -- check WorkingDirectory and any --db flag override) would make every database write fail under ProtectSystem=strict, likely surfacing as a boot-time or first-write crash rather than a clean startup failure -- verify the DB path explicitly, do not assume WorkingDirectory covers it.
- SystemCallFilter=@system-service may still be too narrow if the binary shells out to ffmpeg/taglib subprocesses that themselves need syscalls outside that group -- if the smoke test shows a subprocess failure, widen to @system-service plus the specific missing group (e.g. @process) rather than removing the filter entirely.
- The Whisper port disagreement (TODO says :19847, deploy/local.conf.example's placeholder says :8000) must be resolved with the owner before the allowlist is written -- shipping the wrong port silently breaks transcription exactly the way the TODO warns against.

## Tests

- No Go test applies (systemd unit files are not exercised by `go test`). Verification is operational: after deploying, run `systemd-analyze security audiobook-organizer` on the host and confirm the exposure score improves; then exercise the service's own AI-dependent paths (a metadata fetch hitting OpenLibrary/AcoustID, a transcription job hitting the Whisper host, an AI-scan job hitting Ollama) and confirm none of them fail with a permission-denied or network-unreachable error that traces back to the new SystemCallFilter or IPAddressAllow rules.

Anti-over-suppression test: `N/A -- this is a hardening addition, not a filter/guard/skip on application logic` — a known-good input still passes with the new guard active.

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -c 'ProtectSystem=strict\|ReadWritePaths\|CapabilityBoundingSet\|SystemCallFilter\|IPAddressDeny' deploy/audiobook-organizer.service deploy/systemd/audiobook-organizer.service returns 5 in each file (or more, if IPAddressAllow lines are counted separately)
- [ ] systemctl daemon-reload && systemctl restart audiobook-organizer succeeds with no unit-load errors on a test host
- [ ] a metadata fetch (OpenLibrary/AcoustID), a Whisper transcription call, and an Ollama AI-scan call each succeed after the restart -- the TODO's own caveat: 'an over-tight rule silently breaks metadata and transcription, so test before claiming it works'
- [ ] Anti-over-suppression test: `N/A -- this is a hardening addition, not a filter/guard/skip on application logic` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_194.md`.

## Commit message

```
feat(docs): Harden the systemd unit: ProtectSystem=strict, ReadWritePath (TODO-SEC-SYSTEMD)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`grep -c 'ProtectSystem=strict\|ReadWritePaths\|CapabilityBoundingSet\|SystemCallFilter\|IPAddressDeny' deploy/audiobook-organizer.service deploy/systemd/audiobook-organizer.service returns 5 in each file (or more, if IPAddressAllow lines are counted separately)`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: an over-tight SystemCallFilter or IPAddressAllow silently breaks metadata fetching and transcription in production with no compile-time or CI signal -- the TODO itself flags this risk explicitly. Must be tested live before being considered done, not just merged on the strength of a syntactically valid unit file. Shares both target files with todo_line 10077 (TODO-SEC-BIND) -- coordinate so the two items don't land as conflicting parallel edits to the same two files; recommend doing both in one PR/worktree once 10077's design question is resolved, or landing 10094 first since it has no open design question of its own (only the port-number sub-decision noted above).
