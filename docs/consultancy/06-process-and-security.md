<!-- file: docs/consultancy/06-process-and-security.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4e1100f3-fcf8-47e2-86d7-d63f0dc1d80a -->
<!-- last-edited: 2026-07-17 -->

# Consultancy Evaluation — Process, Ops & Security (2026-07-02)

This report was produced by a read-only multi-agent consultancy workflow evaluating the audiobook-organizer repository. Three specialist agents covered security/PII (SEC-), process/CI/docs (PROC-), and ops/cost (OPS-). All findings cite `file:line` locations verified against the working tree at evaluation time. No files outside this report were modified.

## Executive Summary

The security and process posture is materially **better than the stale June project memory suggests** — a central deliverable of this dimension is status reconciliation. Items SEC-2 ("auth disabled by default"), SEC-7 ("/cache/stats unauthenticated"), and PERF-7, all listed as open in the June audit-remediation memory, are **verified DONE**: `enable_auth` defaults true (`internal/config/config.go:519`), a WARN fires when auth is disabled (`internal/server/server_lifecycle.go:974`), `/cache/stats` sits behind `PermLibraryView` (TODO.md:1405), and the June-audit SEC-2 was renamed SEC-2-AUD (WriteStartupReadOnlyKey flag, default true, TODO.md:1406) after the ID collision was resolved in commit df2bd6f9. ARCH-4b (waves 1–3) and PERF-2b are likewise closed. **Nobody should re-fix these.** The genuinely open residue is small: SEC-AUDIT-11 (a GitHub-console CodeQL alert dismissal) and API-key rotation — which has **zero TODO.md tracking** despite bootstrap-issued keys being full-scope and permanent.

The bootstrap auth flow is well-engineered post pen-test: raw tokens are never logged (CRIT-1 fixed), 10-minute TTL, hash-at-rest, one-time consume under mutex, per-IP rate limiting, and `SetTrustedProxies(nil)` blocking XFF spoofing. Remaining security gaps are the untracked key-rotation item, a pre-commit hook that claims to protect `.claude/.credentials/` but structurally cannot, weak (`$RANDOM`, ~27-bit) per-worktree credential passwords, internal-infrastructure PII saturating 27+ tracked files (a public-release blocker, acceptable while private), and a single `@main`-pinned reusable security workflow violating the repo's own SHA-pin policy.

On process: the deflaking pattern is durable (root-cause fixes with regression proof, codified "never rerun-and-ignore" rule), the mockery v2-vs-v3 CI drift is resolved (though two docs still teach the old pin), and the 30% coverage gate exists but is weak — duplicate test run, swallowed output, static floor. Documentation drift is real: AI-REFERENCE.md claims 189 API routes against ~395 actual gin registrations and omits the Ollama/bge-m3 cutover entirely. **PROC-1 carries an explicit verdict: the untracked `docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md` must be committed — and that commit is being executed in this very PR.**

On ops: the deploy recipe (`Makefile.local` + `deploy/local.conf`, both gitignored) exists only on one laptop with no rollback path; the Windows GPU box at 172.16.3.22 is a triple single-point-of-failure (Whisper, embeddings, LLM) kept alive by an interactive-session scheduled task whose setup scripts live only in a scratchpad; the deprecated nightly `dedup.embed-async` op is still cron-scheduled against the quota-exhausted OpenAI Batch API; there is no cost/quota monitoring (the OpenAI exhaustion was discovered via runtime 429s); and `/metrics` exists but nothing scrapes it — prod observability is manual journalctl. The recurring meta-pattern is operational knowledge landing outside git.

## Findings Table

| ID | Severity | Impact | Effort | Title |
|----|----------|--------|--------|-------|
| SEC-1 | high | high | low | API-key rotation has zero tracking; bootstrap-issued full-scope keys never expire |
| SEC-2 | medium | medium | low | Pre-commit hook claims to protect .claude/.credentials/ but does not |
| SEC-3 | medium | medium | medium | Repo leaks internal infrastructure PII in 27+ files — public-release blocker |
| SEC-4 | low | low | low | Per-worktree credential passwords generated with non-crypto $RANDOM (~27 bits entropy) |
| SEC-5 | low | low | low | Reusable security workflow pinned to @main, violating the repo's SHA-pin policy |
| SEC-6 | info | low | low | Status reconciliation: SEC-2/SEC-7/PERF-7 verified done; only SEC-AUDIT-11 and rotation remain open |
| PROC-1 | high | high | low | Commit docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md — verdict: COMMIT NOW |
| PROC-2 | medium | medium | low | Deprecated nightly dedup.embed-async still cron-scheduled against quota-exhausted OpenAI Batch API |
| PROC-3 | medium | medium | low | AI-REFERENCE.md drift: route count off by ~2x and no mention of the Ollama/local-embedding cutover |
| PROC-4 | low | medium | medium | 30% coverage gate is real but weak: duplicate test run, swallowed output, static low bar |
| PROC-5 | low | medium | low | Mockery pin drift is resolved in CI, but two docs still teach the old v2.53.6 pin and local targets don't enforce the version |
| PROC-6 | medium | medium | low | API-key rotation has zero TODO.md tracking; stale memory reconciled |
| PROC-7 | low | low | low | security.yml is the only workflow not SHA-pinned (uses @main) |
| PROC-8 | info | low | low | Deflaking pattern is durable; installed pre-commit hook has drifted from the setup script |
| OPS-1 | high | high | low | Deploy recipe is single-machine and has no rollback |
| OPS-2 | high | high | medium | Windows GPU box is a triple single-point-of-failure held up by an interactive-session scheduled task |
| OPS-3 | high | medium | low | Nightly dedup.embed-async still hardwired to OpenAI Batch API after Ollama cutover |
| OPS-4 | medium | medium | medium | No cost/quota monitoring — OpenAI exhaustion was discovered by production failure |
| OPS-5 | medium | medium | medium | /metrics exists but nothing scrapes it — no alerting layer at all |
| OPS-6 | medium | medium | low | Recurring pattern: operational knowledge lands outside git |
| OPS-7 | low | low | low | Agent dev-workflow is strong but CLAUDE.md triplicates rules and burns per-session tokens |

## Security & PII Findings (SEC-)

### SEC-1 — API-key rotation has zero tracking; bootstrap-issued full-scope keys never expire (high)

**Detail:** `handleBootstrap` mints an API key with scopes = `auth.All()` and no `ExpiresAt` (bootstrap.go:340-349 sets ID/UserID/Name/TokenHash/Scopes/Status/CreatedAt only) — every bootstrap exchange produces a permanent god-key. Revocation exists (`DELETE /auth/api-keys/:id`, apikeys.go:314) and Create supports optional expiry, but there is no rotation endpoint, no max-age enforcement, and no re-key workflow. A grep for "rotation" in TODO.md returns nothing — the item the June audit memory lists as remaining is genuinely untracked. Given the `.claude/.api-token` file is shared across worktrees and repeatedly re-bootstrapped by agents, accumulated live full-scope keys in PebbleDB are the residual risk.

**Recommendation:** Add a TODO.md item. Minimal fix: set `ExpiresAt` (e.g. 30d) on bootstrap-issued keys in `handleBootstrap`, and add a startup sweep that flags/expires active full-scope keys older than N days. Optional: `POST /auth/api-keys/:id/rotate` that atomically issues a new token and revokes the old.

**Citations:**
- internal/server/bootstrap.go:338
- internal/server/bootstrap.go:340-349
- internal/server/handlers/apikeys.go:314
- internal/database/apikey_token.go:19-28

### SEC-2 — Pre-commit hook claims to protect .claude/.credentials/ but does not (medium)

**Detail:** The installed hook's `PROTECTED_FILES` array (setup-git-hooks.sh:17-22) contains only `.api-token`, `.claude/.api-token`, `.bootstrap-token`, `.readonly-key`. The success message (line 53) tells the user ".claude/.credentials/ (per-worktree username/password)" is protected, but the exact-match grep `^$FILE$` (line 27) can never match files inside a directory, and the directory is not in the array. `.gitignore:239` covers it as the primary defense, but a `git add -f` (which agents sometimes do to bypass ignore warnings) sails through the hook that the docs claim would block it. The same exact-match logic also misses these files staged from subdirectories.

**Recommendation:** Add `.claude/.credentials/` to the array and change the match to a prefix test (`grep -q "^$FILE"` or case pattern), or match basenames anywhere in the tree. Add a generic content check for `abk_`/`abbs_` prefixes in staged diffs while there.

**Citations:**
- scripts/setup-git-hooks.sh:17-22
- scripts/setup-git-hooks.sh:27
- scripts/setup-git-hooks.sh:53
- .gitignore:239

### SEC-3 — Repo leaks internal infrastructure PII in 27+ files — public-release blocker (medium)

**Detail:** `git grep` finds 172.16.x.x private IPs in 27 tracked files (docs/system/runbooks.md, incidents.md, scripts/transcribe_monitor.py, setup-ssh-from-mac.sh, skills/project-context/SKILL.md, docs/status/...) and 61 files containing the internal hostname "unimatrixzero" or `/mnt/bigdata` paths. Archived plans embed literal `ssh jdfalk@unimatrixzero.local` and `scp ... jdfalk@172.16.2.30` commands (failed-quarantine.md:1361-1362), exposing SSH username + host + home-dir layout. The new untracked docs/status/ file adds the Ollama box (172.16.3.22:11434). No live tokens or personal emails were found (docs use `abk_...` placeholders); `certs/localhost.key` is a documented snake-oil dev cert (certs/README.md warns explicitly). Risk is contingent: acceptable for a private repo, a blocker if published — and agents/pii-scanner.md exists precisely because this was anticipated.

**Recommendation:** Keep the repo private, or before any public release run the existing pii-scanner agent and sweep: replace IPs/hostnames/usernames with `<your-server-ip>`/`<your-hostname>`/`<your-username>`, prioritizing docs/system/, docs/status/, scripts/, skills/. Note git history would also need rewriting.

**Citations:**
- docs/system/runbooks.md:61
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:11
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:43
- docs/archive/superpowers/plans/2026-04-22-failed-quarantine.md:1361-1375
- skills/project-context/SKILL.md:1

### SEC-4 — Per-worktree credential passwords generated with non-crypto $RANDOM (~27 bits entropy) (low)

**Detail:** `generate_password` picks 3 words from a 25-word list plus a 4-digit number using bash `$RANDOM` (a weak 15-bit LCG, often time-seeded): ~log2(25³ × 9000) ≈ 27 bits at best, less given `$RANDOM` predictability. These are real API-login credentials, typically used against localhost but `api_host` is configurable. Contrast: the Go server's `generateReadablePassword` (bootstrap.go:454-466) does the same word scheme but with `crypto/rand` and a 64-word list. Separately, `cred_file` (manage-credentials.sh:41-44) joins the raw branch name into the path — a branch like `feature/x` or `../x` escapes or fails outside `.claude/.credentials/` (username is sanitized at line 60, the filename is not).

**Recommendation:** Generate the password from `/dev/urandom` (e.g. base64 of 16 bytes, or `openssl rand`) and sanitize the branch name for the filename the same way it is sanitized for the username.

**Citations:**
- scripts/manage-credentials.sh:22-38
- scripts/manage-credentials.sh:41-44
- internal/server/bootstrap.go:454-466

### SEC-5 — Reusable security workflow pinned to @main, violating the repo's SHA-pin policy (low)

**Detail:** A scan of `.github/workflows/*.yml` for non-SHA action refs finds exactly one: `falkcorp/github-common/.github/workflows/reusable-security.yml@main`. Everything else is pinned to 40-char SHAs, consistent with the repo's stated mandatory policy ("Pin all GitHub Action references to SHAs, not tags" in CLAUDE.md) and the recent burndown re-pin work (commit 28427b6f). The mutable `@main` ref is first-party (own org), which limits supply-chain exposure to a compromise of that repo — but it is the security workflow itself, so a malicious change there runs with this repo's secrets, and it contradicts the policy the repo enforces elsewhere.

**Recommendation:** Pin reusable-security.yml to a commit SHA like the other refs; include it in the nightly-burndown re-pin routine so it doesn't drift stale.

**Citations:**
- .github/workflows (uses: falkcorp/github-common/.github/workflows/reusable-security.yml@main)
- CLAUDE.md GitHub Operations section

### SEC-6 — Status reconciliation: SEC-2/SEC-7/PERF-7 verified done; only SEC-AUDIT-11 and rotation remain open (info)

> **Reconciliation notice — do not re-fix these items.** The stale June memory listed SEC-2, SEC-7, and PERF-7 as open. All three were verified DONE in this evaluation.

**Detail:** The ID collision is resolved: original SEC-2 (auth-enabled default, TODO.md:1275) is done — `enable_auth` defaults true (config.go:519) and a WARN fires when disabled (server_lifecycle.go:974; the `:851` line ref in TODO.md is stale, code moved). The June-audit SEC-2 was renamed SEC-2-AUD (WriteStartupReadOnlyKey flag, default true, TODO.md:1406) and is shipped, as is SEC-7 (/cache/stats behind PermLibraryView, TODO.md:1405). ARCH-4b and PERF-2b listed as remaining in the June memory are also closed (TODO.md ARCH-4b waves 1-3, PERF-2b entries). Genuinely open: SEC-AUDIT-11 (TODO.md:1221 — CodeQL alert dismissal, a GitHub-console action, code work done) and API-key rotation (SEC-1 above, untracked). /metrics remains unauthenticated at server_lifecycle.go:907 by documented accepted risk (MED-1, noted at TODO.md:1405). The bootstrap flow itself is solid: no raw-token logging (CRIT-1 fixed, bootstrap.go:98-111,157-162), 10-min TTL, one-time consume, 5/hr/IP limit, `SetTrustedProxies(nil)` at server.go:351 prevents XFF rate-limit bypass.

**Recommendation:** Update the stale June memory/audit doc to match TODO.md; add a one-line TODO entry for SEC-AUDIT-11's console action and for key rotation so nothing lives only in memory files.

**Citations:**
- TODO.md:1275
- TODO.md:1405-1406
- internal/config/config.go:519
- internal/server/server_lifecycle.go:974
- TODO.md:1221
- internal/server/server_lifecycle.go:907

## Process, CI & Documentation Findings (PROC-)

### PROC-1 — Commit docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md — verdict: COMMIT NOW (high)

> **Note:** This verdict is being executed in this very PR — the status doc is committed as part of the consultancy change set.

**Detail:** The file is untracked yet is the only written record of production-critical operational knowledge: the prod embedding config (base_url 172.16.3.22:11434, bge-m3, 1024-dim), the fact that Ollama survives only via the interactive-session scheduled task "OllamaServe", the PowerShell `-EncodedCommand` gotcha, and re-embed state. Its own Pending section (line 75) notes the Windows Ollama setup scripts exist only in a scratchpad. If the working tree or scratchpad is lost, reconstructing the local-backend cutover requires reverse-engineering a Windows box. The docs/status/ pattern itself is sound (dated, versioned header) — but only if tracked.

**Recommendation:** Commit docs/status/ immediately (docs commit, no code risk). In the same change, commit the Windows Ollama scripts as `scripts/manage-ollama-windows.py` per the file's own TODO, so the doc and the automation land together.

**Citations:**
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:30-47
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:37-38
- git status: `?? docs/status/`

### PROC-2 — Deprecated nightly dedup.embed-async still cron-scheduled against quota-exhausted OpenAI Batch API (medium)

**Detail:** `embedAsyncDef` registers `dedup.embed-async` with Schedule `"0 3 * * *"` (nightly 03:00) and its own DisplayName marks it "[deprecated — use embed-scan with async:true]". It delegates to `runEmbedScanMode(async=true)`, which per the status doc uses the OpenAI Batch API — the backend that is 429 insufficient_quota. Every night this op fires, fails against a dead backend, and generates error noise that can mask real failures; `ResumeRequeue` means it also re-queues after restarts. The live cutover path (`dedup.embed-scan` sync) does not need it.

**Recommendation:** Remove or nil the Schedule on embed-async (or gate scheduling on the embedding backend being OpenAI and quota-healthy). Since the op is self-described as deprecated, deleting the nightly cron is the low-risk option; keep the op invocable manually.

**Citations:**
- internal/plugins/dedup/embed_async.go:24
- internal/plugins/dedup/embed_async.go:27-36
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:55-57

### PROC-3 — AI-REFERENCE.md drift: route count off by ~2x and no mention of the Ollama/local-embedding cutover (medium)

**Detail:** The header claims "Last updated: 2026-07-01 | Total API routes: 189", but a count of gin `.GET/.POST/.PUT/.DELETE/.PATCH` registrations across non-test files in internal/server (including wire_*_routes.go) yields ~395. Grep for "ollama" or "bge-m3" in AI-REFERENCE.md returns nothing; the doc still presents OpenAI as the AI backend (line 31 "→ AI parser → OpenAI API"; line 85 OpenAIParser; line 451 "AUTHOR dedup via OpenAI batch API"). Since the file's stated purpose is "Read this before making any changes" and every agent session loads it via the project-context skill, drift here propagates wrong assumptions into every automated task.

**Recommendation:** Update the route count (or replace the hardcoded number with a "see wire_*_routes.go" pointer generated by a make target), and add an "Embedding/LLM backends" section covering the Ollama primary, bge-m3 1024-dim, and the stale embeddings.db SQLite. Consider a lightweight CI drift check comparing the header route count against a grep count.

**Citations:**
- docs/AI-REFERENCE.md:6
- docs/AI-REFERENCE.md:31
- docs/AI-REFERENCE.md:85
- docs/AI-REFERENCE.md:451

### PROC-4 — 30% coverage gate is real but weak: duplicate test run, swallowed output, static low bar (low)

**Detail:** `coverage-check-short` re-runs `go test ./... -short` with `>/dev/null 2>&1` (Makefile:311) even though `make ci` (line 321) already ran test-all-short — doubling CI wall time — and any compile/test failure at the gate step produces no diagnostics, just a make failure with an empty screen. The 30% total-percentage threshold is a floor, not a signal: it cannot regress meaningfully (a large low-coverage package addition barely moves total %) and cannot catch a specific package dropping to 0%. It is meaningful only as a "tests still compile and run" tripwire.

**Recommendation:** Have test-all-short emit coverage.out and make coverage-check-short consume it instead of re-running tests; drop the output redirect so failures are visible. Longer term, replace the flat 30% with a ratchet (fail if coverage drops >0.5% from committed baseline) or per-package floors on the packages that matter (dedup, database, server/handlers).

**Citations:**
- Makefile:309-318
- Makefile:321

### PROC-5 — Mockery pin drift is resolved in CI, but two docs still teach the old v2.53.6 pin and local targets don't enforce the version (low)

**Detail:** CI now installs mockery v3.7.1 (ci.yml:83) matching Makefile comments and setup-mockery.sh — the v2/v3 drift in project memory is stale/fixed (PR #1718, CHANGELOG:62). Two residues: (1) docs/agent-tasks/ci-flaky-fixes/README.md still tells agents that CI pins "mockery/v2@v2.53.6" — an agent following that brief would reintroduce the exact repo-wide mock-churn footgun the brief warns about; (2) `make mocks` and `mocks-check` invoke whatever `mockery` is on PATH with only a comment ("check mockery version", Makefile:193) as protection — the historical failure mode was precisely a local binary at the wrong version.

**Recommendation:** Update ci-flaky-fixes/README.md (TASK-01 is done — archive it). Add a version guard to the mocks/mocks-check targets: fail fast if `mockery version` != v3.7.1, or invoke via `go run github.com/vektra/mockery/v3@v3.7.1` so the pin is self-enforcing.

**Citations:**
- .github/workflows/ci.yml:83
- Makefile:194-196
- Makefile:203-205
- scripts/setup-mockery.sh:8
- docs/agent-tasks/ci-flaky-fixes/README.md:24-31

### PROC-6 — API-key rotation has zero TODO.md tracking; stale memory reconciled (medium)

> **Reconciliation notice (duplicate confirmation of SEC-6):** SEC-2, SEC-7, SEC-2-AUD, PERF-7, and PERF-2b are all marked done in TODO.md — the June memory listing them open is stale. Do not re-fix.

**Detail:** Reconciliation confirmed: SEC-2 (auth-disabled WARN, TODO.md:1275), SEC-7 (/cache/stats behind PermLibraryView, TODO.md:1405), SEC-2-AUD (WriteStartupReadOnlyKey flag default true, TODO.md:1406), PERF-7 (TODO.md:1404) and PERF-2b (TODO.md:1398) are all marked done — the June memory file listing them open is stale. The genuinely open items are ARCH-4b residual (acoustid/reset_all.go, TODO.md:38 and the 2026-07-01 annotation at TODO.md:1395) and API-key rotation, which is completely untracked: grep for "rotation"/"rotate" in TODO.md returns nothing. Given `.claude/.api-token` is shared across worktrees with only an 8-hour auto-cleanup convention, rotation is a real residual audit item with no owner.

**Recommendation:** Add a SEC-9 (or similar) TODO.md entry for API-key rotation/expiry (server-side key TTL + rotation endpoint), so the burndown bot can pick it up. Update the June audit-remediation memory note to reflect PERF-7/SEC-7/SEC-2 closure.

**Citations:**
- TODO.md:1275
- TODO.md:1404-1406
- TODO.md:38
- TODO.md:1395

### PROC-7 — security.yml is the only workflow not SHA-pinned (uses @main) (low)

**Detail:** All other workflow refs are SHA-pinned with version comments (e.g. ci.yml:31, nightly-burndown.yml:28 re-pinned in commit 28427b6f, frontend-ci.yml:45). security.yml:30 references `falkcorp/github-common/.github/workflows/reusable-security.yml@main` — a mutable ref that violates the repo's own mandatory SHA-pinning rule (CLAUDE.md "Pin all GitHub Action references to SHAs, not tags"). Ironically it is the security workflow itself. Reusable workflows at @main also break reproducibility of past runs. (Same underlying issue as SEC-5, reported from the process angle.)

**Recommendation:** Pin reusable-security.yml to the current github-common main SHA with a version comment, matching the pattern used in the other 8 workflows.

**Citations:**
- .github/workflows/security.yml:30

### PROC-8 — Deflaking pattern is durable; installed pre-commit hook has drifted from the setup script (info)

**Detail:** Positive: flaky tests are fixed at root cause with regression proof (dead os.Chdir race #1711; missing WaitForWarmup #1713; June 17/19 memdb-warmup flakes fixed at source), the rule "do not rerun-and-ignore, prove with -count=20" is codified in the workstream brief, and the residual known race (PEBBLE-CLOSED-SHUTDOWN-RACE) was filed then fixed (commit 5b90d2f6 lineage). One hygiene gap: the installed `.git/hooks/pre-commit` protects .api-token/.bootstrap-token but lacks `.readonly-key`, which the current scripts/setup-git-hooks.sh includes — hooks are copied, not referenced, so script updates never reach installed hooks.

**Recommendation:** Re-run scripts/setup-git-hooks.sh in each checkout/worktree, or better, switch to `git config core.hooksPath scripts/hooks` with a versioned hook so updates propagate automatically.

**Citations:**
- docs/agent-tasks/ci-flaky-fixes/README.md:21-23
- CHANGELOG.md:62
- CHANGELOG.md:75
- scripts/setup-git-hooks.sh:17-22
- .git/hooks/pre-commit:4-8

## Ops & Cost Findings (OPS-)

### OPS-1 — Deploy recipe is single-machine and has no rollback (high)

**Detail:** `make deploy` (Makefile.local:13-21) cross-compiles, scp's the binary, then `sudo mv` overwrites /usr/local/bin/audiobook-organizer and restarts systemd. No copy of the previous binary is kept, there is no rollback/versioned-release target — rollback means rebuilding an older commit locally. Both Makefile.local and deploy/local.conf are gitignored (verified via `git check-ignore`), yet local.conf explicitly says "It is NOT a template — it reflects the actual prod server" — the full prod ExecStart (TLS paths, DB path, WHISPER_REMOTE_URL, TimeoutStopSec) exists only on this one laptop. Loss of the machine loses the deploy pipeline.

**Recommendation:** Commit a sanitized deploy/local.conf template plus a documented deploy runbook; add a deploy step that keeps the prior binary as /usr/local/bin/audiobook-organizer.prev and a `make rollback` target that swaps it back and restarts. Consider stamping deployed version (git describe is already in ldflags) into a /health field for verification.

**Citations:**
- Makefile.local:13-21
- deploy/local.conf:10-11

### OPS-2 — Windows GPU box is a triple single-point-of-failure held up by an interactive-session scheduled task (high)

**Detail:** 172.16.3.22 now serves three production dependencies: Whisper transcription (WHISPER_REMOTE_URL in local.conf, itself started manually per a comment: `$env:WHISPER_PORT=...; uv run whisper_server.py`), bge-m3 embeddings, and the qwen2.5 LLM. Ollama survives only via scheduled task "OllamaServe" bound to an interactive session for GPU access — a logoff, reboot, or Windows Update silently kills embeddings and LLM. The setup/start scripts (setup-ollama.ps1, start-ollama.ps1) exist only in a session scratchpad (status doc lines 37-38, explicit TODO). Whisper's launcher appears equally ad-hoc. There is no health check on the server side alerting when 172.16.3.22 goes dark; embed failures would surface only as op errors in journalctl.

**Recommendation:** Immediately commit the Windows management scripts as scripts/manage-ollama-windows.py (already TODO'd). Configure the scheduled task with "run whether user is logged on or not" + restart-on-failure, or move Ollama to a Windows service wrapper (NSSM). Add a periodic reachability probe of :11434 and :19847 exposed via /metrics so an outage is visible.

**Citations:**
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:30-38
- deploy/local.conf:26-28

### OPS-3 — Nightly dedup.embed-async still hardwired to OpenAI Batch API after Ollama cutover (high)

**Detail:** embed_async.go:36 schedules `"0 3 * * *"` and delegates to `runEmbedScanMode(async=true)`, which calls `EmbedBooksAsync` → `CreateEmbeddingBatch` against the OpenAI Batch API (Ollama's /v1 has no Batch endpoint). With OpenAI quota exhausted this fails every night at 03:00 ("submit embedding batch: 429") — pure ops noise. Worse: if quota is ever restored while the in-flight bge-m3 re-embed is incomplete, the batch would ingest 3072-dim OpenAI vectors into the 1024-dim index. The async skip check (engine.go:2306-2309) compares only TextHash, not embedding model — the model-aware skip shipped in #1738 applies to the sync path only, so the two paths disagree on what "needs embedding" means. (Overlaps PROC-2; this version adds the dimension-contamination risk.)

**Recommendation:** Gate the nightly async op on the configured embedding backend: when embedding.base_url points at a non-OpenAI backend, either no-op with a logged skip or delegate to the sync path. Make the async skip model-aware to match #1738. Fold this into the pending LLM backend-mode toggle work.

**Citations:**
- internal/plugins/dedup/embed_async.go:24-38
- internal/dedup/engine.go:2306-2323
- internal/plugins/dedup/embed_scan.go:73-90

### OPS-4 — No cost/quota monitoring — OpenAI exhaustion was discovered by production failure (medium)

**Detail:** The cutover was forced, not planned: 429 insufficient_quota at runtime is the only spend signal that exists. A repo-wide search finds no cost, spend, or pricing tracking in code or docs — no per-op token accounting, no budget threshold, no quota alert. The economics actually favor local for this workload (a full ~29K-book re-embed on an owned RTX 2060 Super costs ~zero marginal dollars, and embedding text is short metadata strings), but the trade was paid in fragility (OPS-2) and unquantified quality (bge-m3 1024-dim vs text-embedding-3-large 3072-dim — no dedup-recall comparison recorded). The pending backend-mode toggle (status doc, Pending #1) is the right vehicle for an OpenAI-fallback posture but has no code yet.

**Recommendation:** Keep local as primary — the economics are sound. Add: (a) a token/request counter per AI backend exposed on /metrics, (b) the backend-mode toggle with OpenAI+local-fallback so a restored OpenAI key is a config change not a code change, (c) a one-time dedup-recall spot-check comparing bge-m3 vs archived OpenAI embeddings before deleting legacy vectors.

**Citations:**
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:10-14
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:68-77

### OPS-5 — /metrics exists but nothing scrapes it — no alerting layer at all (medium)

**Detail:** server_lifecycle.go:907 registers an unauthenticated promhttp handler with a well-reasoned accepted-risk comment (pen-test MED-1) — that part is fine for a LAN-only server. But the deploy/ directory contains only service files; there is no Prometheus scrape config, no Grafana, no alertmanager, no healthcheck cron anywhere in the repo. Effective prod observability is manual journalctl pulls via the server-logs skill. Consequences already observed: OpenAI quota exhaustion, the 69GB cache-warmup bloat, and op wedges were all discovered by humans noticing symptoms, not by alerts. The nightly embed-async failures (OPS-3) will similarly rot silently.

**Recommendation:** Stand up a minimal scrape+alert loop on 172.16.2.30 (single prometheus + alertmanager container, or even a cron curl of /metrics + /health with a threshold script). Alert on: service restarts, op failure counts, RSS above GOMEMLIMIT fraction, and reachability of 172.16.3.22 endpoints (per OPS-2).

**Citations:**
- internal/server/server_lifecycle.go:897-907
- deploy/local.conf:1-36

### OPS-6 — Recurring pattern: operational knowledge lands outside git (medium)

**Detail:** Three independent instances of the same failure mode: (1) docs/status/ is untracked (`?? docs/status/` in git status) — the authoritative cutover record could be lost to a `git clean`; (2) Ollama setup scripts live only in a session scratchpad (status doc explicit TODO); (3) the deploy recipe is in gitignored Makefile.local whose header says "Local-only targets — not committed". Combined with per-worktree credentials in .claude/.credentials/ and .api-token, the bus factor for operating prod is one laptop. The agent memory system partially compensates (MEMORY.md index) but memory files are also machine-local.

**Recommendation:** Commit docs/status/ now (it already has a proper version header — executed in this PR, see PROC-1). Establish a rule: any artifact referenced by prod (scripts, drop-ins, runbooks) must exist in-repo, with only secrets externalized. Track OPS-1's local.conf template under this same item.

**Citations:**
- docs/archive/2026-07-consolidation/status/2026-07-02-local-cutover-and-matching.md:37-38
- Makefile.local:1-6

### OPS-7 — Agent dev-workflow is strong but CLAUDE.md triplicates rules and burns per-session tokens (low)

**Detail:** The workflow itself is a strength: worktree-only discipline, parallel-sweep with per-task worktrees and sibling-rebase, honest status-count reporting, and a memory index that front-loads gotchas (memdb round-trip footgun, mockery drift). The inefficiency is structural: CLAUDE.md states the worktree/PR rule in three separate sections ("Worktree Discipline", "Workflow Discipline", "Quick Fix Workflow") and repeats plan-first twice, all loaded verbatim into every session for every agent — including narrow read-only subagents that never edit. Duplicated rules also drift independently (the three sections already differ on details like PLAN.md placement). Known breakdown points already documented in memory: mockery v3.7.1 vs pinned v2.53.6 regenerating all mocks, and overlapping sweep waves conflicting on rebase.

**Recommendation:** Deduplicate CLAUDE.md to one canonical statement per rule with cross-references; move verbatim prompt templates to docs/ or skills. Pin mockery via a tools.go/mise version so local and CI cannot drift. No change needed to the sweep architecture itself.

**Citations:**
- CLAUDE.md:1-200

## Steelman and Design

The specialist reports for this dimension included no populated steelman or design sections (the ops report carried empty placeholders). No content omitted.

## Cross-Report Overlaps

- **SEC-5 = PROC-7:** same `@main`-pinned reusable-security.yml; fix once.
- **PROC-2 ⊂ OPS-3:** same nightly embed-async cron; OPS-3 adds the 3072-dim vs 1024-dim index-contamination risk if OpenAI quota is restored mid-re-embed.
- **SEC-1 = PROC-6 (rotation half):** API-key rotation untracked; one TODO.md entry closes both.
- **SEC-6 = PROC-6 (reconciliation half):** independent confirmations that SEC-2/SEC-7/PERF-7 (and SEC-2-AUD, PERF-2b) are done.
- **PROC-1 = OPS-6 item (1):** committing docs/status/ resolves both; executed in this PR.
- **SEC-2 ↔ PROC-8:** both concern pre-commit hook gaps — SEC-2 the credentials-directory match logic, PROC-8 the copied-hook drift; a hooksPath-based versioned hook fixes both.
