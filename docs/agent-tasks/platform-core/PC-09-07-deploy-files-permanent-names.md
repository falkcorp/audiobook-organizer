<!-- file: docs/agent-tasks/platform-core/PC-09-07-deploy-files-permanent-names.md -->
<!-- version: 1.0.0 -->
<!-- guid: e29d0340-2fd8-4827-9c2a-35762c1a9e7a -->
<!-- last-edited: 2026-11-04 -->

# PC-09-07 — Deploy files keep only permanent legacy names; compose passes the Basic variables through only when set

**ID:** PC-09-07 · **Wave:** W09 (S3, release `S3`) · **Priority:** P1 · **Effort:** M
**Tier:** T1 Sonnet/medium — four config files and one scanning test; every edit is listed below.
**Depends on:** PC-09-06 (the S3 flip; this PR follows it in the same release).
**Stage-Release trailer:** `Stage-Release: S3 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S3.md` (Behaviour rule 23).
**Owning spec:** 09 S3 "Deploy files"; 05-config §1.12.4 (permanent rows, the two not-alias lists,
`TestShippedDeployEnvNamesArePermanent`); 09 §9.2.5; 14 Q-F.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-09-07" -b agent/pc-09-07-deploy-env-names origin/main
cd "$REPO/../audiobook-organizer-pc-09-07"
git rebase origin/main
git worktree list
npm ci --prefix web
```

## Step 0 — read the owner's OLLAMA_BASE_URL decision (STOP if absent)

```bash
grep -rn 'OLLAMA_BASE_URL' docs/plans/platform-core/stage-ledger/ 2>/dev/null
```

Expected: one line in the S2b or S3 stage record written by the owner at OWNER-08, saying either **delete** or
**replace with `<NAME>`**. No hit = STOP and report "OWNER-08's OLLAMA_BASE_URL decision is not recorded". Use the
decision exactly in step 4.

## Exact files

Create: `internal/config/deploy_env_names_test.go`.
Modify: `deploy/audiobook-organizer.service`, `deploy/com.audiobook-organizer.plist`, `docker-compose.yml`,
`deploy/local.conf.example`.

## Re-verify (expected at the plan checkout `f07f516be`; if a count differs at your base, STOP and report)

```bash
grep -c 'Environment' deploy/audiobook-organizer.service                      # 6
grep -n 'Environment="AUDIOBOOK_ROOT_DIR=' deploy/audiobook-organizer.service # 1 hit (line 97)
grep -n '<key>AUDIOBOOK_ROOT_DIR</key>' deploy/com.audiobook-organizer.plist  # 1 hit (line 38)
grep -n 'AUDIOBOOK_ROOT_DIR' deploy/com.audiobook-organizer.plist             # 3 hits (lines 10, 35, 38)
grep -n -- '- AO_DIR=\|- AO_DB=' docker-compose.yml                           # 2 hits (lines 18-19)
grep -n 'BASIC_AUTH_ENABLED=\${BASIC_AUTH_ENABLED:-false}' docker-compose.yml # 1 hit (line 20)
grep -n 'BASIC_AUTH_USERNAME=\${BASIC_AUTH_USERNAME:-admin}' docker-compose.yml   # 1 hit (line 21)
grep -n 'BASIC_AUTH_PASSWORD=\${BASIC_AUTH_PASSWORD:-}' docker-compose.yml    # 1 hit (line 22)
grep -n '^Environment=OLLAMA_BASE_URL=' deploy/local.conf.example             # 1 hit (line 40)
grep -n 'Environment' deploy/local.conf.example | wc -l                       # 6 (2 live, 4 commented)
grep -n '^ *ENV ' Dockerfile Dockerfile.build-cgo | wc -l                     # 0
grep -n 'func LegacyEnvPermanent() \[\]string' internal/config/aliases_gen.go # 1 hit (PC-09-03); 0 = STOP
```

## Steps

1. `deploy/audiobook-organizer.service`: delete the single line `Environment="AUDIOBOOK_ROOT_DIR=..."`. Leave `HOME`,
   `DATABASE_PATH`, `GOMEMLIMIT`, `GOGC` and `FP_PARALLEL_WORKERS` exactly as they are. Bump the header version (patch).
2. `deploy/com.audiobook-organizer.plist`: delete the `<key>AUDIOBOOK_ROOT_DIR</key>` line and the `<string>` line
   right after it. In the comment on line 35 change `(edit AUDIOBOOK_ROOT_DIR for your setup)` to
   `(edit DATABASE_PATH for your setup)`. In the install instructions on line 10 change
   `set AUDIOBOOK_ROOT_DIR to your library path` to `set DATABASE_PATH to your database path`. Leave `DATABASE_PATH`.
   Bump the header version.
3. `docker-compose.yml`, `environment:` list only:
   - delete `- AO_DIR=/audiobooks` and `- AO_DB=/data/audiobooks.pebble`;
   - replace the three Basic lines with exactly:
     ```yaml
           - BASIC_AUTH_ENABLED
           - BASIC_AUTH_USERNAME
           - BASIC_AUTH_PASSWORD
     ```
     (same indentation as the lines you replace; no `=` and no default).
   - Do **not** touch the `volumes:` line `${AUDIOBOOK_ROOT_DIR:-./audiobooks}:/audiobooks:ro`: that is compose's host-side
     interpolation, not a container variable. Bump the header version.
4. `deploy/local.conf.example`, the `OLLAMA_BASE_URL` line (line 40):
   - decision **delete**: delete the line; if the comment block above it (lines 34-38) then describes only the deleted
     endpoint, keep it, because `WHISPER_REMOTE_URL` on line 39 still uses it;
   - decision **replace with `<NAME>`**: change only the variable name on that line to `<NAME>`; keep the value as it is.
   Do not copy the example's address values anywhere else. Bump the header version.
5. Create `internal/config/deploy_env_names_test.go` (package `config`, file header with a new guid) with
   `TestShippedDeployEnvNamesArePermanent`:
   - files: `filepath.Glob("../../deploy/*.service")`, `../../deploy/*.plist`, `../../deploy/local.conf.example`,
     `../../docker-compose.yml`, `../../Dockerfile*`;
   - extraction regexes (commented lines included):
     `.service` and `local.conf.example`: `^\s*#?\s*Environment="?([A-Z][A-Z0-9_]*)=`;
     `.plist`: `<key>([A-Z][A-Z0-9_]*)</key>` between `<key>EnvironmentVariables</key>` and the next `</dict>`;
     `docker-compose.yml`: list items under an `environment:` key, `^\s*-\s*([A-Z][A-Z0-9_]*)(=|$)`;
     `Dockerfile*`: `^\s*ENV\s+([A-Z][A-Z0-9_]*)`;
   - allowed set = `LegacyEnvPermanent()` ∪ {`AUDIOBOOK_ROOT_DIR`, `AO_DIR`, `AO_DB`, `OLLAMA_BASE_URL`} ∪ {`HOME`,
     `GOMEMLIMIT`, `GOGC`, `TZ`, `NOTIFY_SOCKET`} (05-config §1.12.4's two not-alias lists); any other name fails with
     `t.Errorf("%s: %s is not a permanent legacy name", file, name)`;
   - second assertion: none of `AUDIOBOOK_ROOT_DIR`, `AO_DIR`, `AO_DB`, `OLLAMA_BASE_URL` is extracted from any file
     (09 S3 "Deploy files": deleted or replaced);
   - third assertion: the compose `environment:` items for the three Basic names have no `=`.
6. Add a `changelog.d/` fragment (no header) describing the deploy-file change for operators (existing users keep their
   copied compose files; `Alias.BelowStored` keeps their UI settings in force).

## Code contract

```go
// internal/config/deploy_env_names_test.go
func TestShippedDeployEnvNamesArePermanent(t *testing.T)
```

## Acceptance (expected output)

```bash
go test ./internal/config -run TestShippedDeployEnvNamesArePermanent -count=1 -v | tail -3   # --- PASS: TestShippedDeployEnvNamesArePermanent ... ok
grep -c 'Environment' deploy/audiobook-organizer.service          # 5
grep -c 'AUDIOBOOK_ROOT_DIR' deploy/com.audiobook-organizer.plist # 0
grep -c 'AO_DIR\|AO_DB' docker-compose.yml                        # 0
grep -n -- '- BASIC_AUTH_\(ENABLED\|USERNAME\|PASSWORD\)$' docker-compose.yml | wc -l   # 3
grep -c 'OLLAMA_BASE_URL' deploy/local.conf.example               # 0
docker compose -f docker-compose.yml config >/dev/null && echo compose-ok   # compose-ok (skip if docker is unavailable; say so in the PR)
make ci                                                           # exit 0
```

## Out of scope

Copying changed files to any host (an owner step, OWNER-10); P4's scan (owner); any alias or loader change.

## Rollback

Revert the PR. Deploy files reach a host only when an owner copies them; older builds read none of the deleted names.

## Repo rules

Bump the header on every changed file; the new test gets a new header and guid. Conventional commit, e.g.
`chore(deploy): keep only permanent legacy env names in shipped files`, with `Stage-Release: S3 1` and ending with
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr create`, then `gh pr merge <n> --rebase` after the gate.
