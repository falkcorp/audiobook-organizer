<!-- file: docs/agent-tasks/todo-completion-2026-09/config/TASK-344-mask-the-remaining-secrets-returned-by-get-api-v.md -->
<!-- version: 1.0.0 -->
<!-- guid: 702c63e1-2809-4828-a8b6-9ed813910b3d -->
<!-- last-edited: 2026-09-10 -->

# TASK-344 — Mask the remaining secrets returned by `GET /api/v1/config` (TODO.md:3344)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` section “Mask the remaining secrets returned by `GET /api/v1/config`”, lines 3344, 3345, 3346, 3347, 3348

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · config subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` section “Mask the remaining secrets returned by `GET /api/v1/config`”, lines 3344, 3345, 3346, 3347, 3348. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/config-344" -b agent/config-344-mask-the-remaining-secrets-returned-by-g origin/main
cd "$REPO/.worktrees/config-344"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 5 still-open `TODO.md` item(s) under section “Mask the remaining secrets returned by `GET /api/v1/config`” (lines 3344, 3345, 3346, 3347, 3348 as of HEAD 42d187168). Each item's own text is the spec; the reconciliation evidence below says what still shows the gap.

## Background (verify before editing)

- L3344 — - [ ] `OAuthGithubClientSecret` and `OAuthGoogleClientSecret` (`internal/config/config.go:895-898`) — evidence: internal/config/update_service.go:82-101 `MaskSecrets` masks only OpenAIAPIKey, AcoustIDAPIKey, GoogleBooksAPIKey, HardcoverAPIToken, BasicAuthPassword, and MetadataSources credentials. `OAuthGithubClientSecret`/`OAuthGoogleClientSecret` (internal/config/config.go:1248,1250) are not touched.
- L3345 — - [ ] `DelugeWebPassword` (`internal/config/config.go:991`) — evidence: internal/config/config.go:1343 `DelugeWebPassword string` field exists; internal/config/update_service.go:82-101 `MaskSecrets` never references it.
- L3346 — - [ ] `DownloadClient.Torrent.Deluge.Password` (`config.go:172`) — evidence: internal/config/config.go:2696 `Password: viper.GetString("download_client.torrent.deluge.password")` populates the field; MaskSecrets (update_service.go:82-101) does not mask it.
- L3347 — - [ ] `DownloadClient.Torrent.QBittorrent.Password` (`config.go:180`) — evidence: internal/config/config.go:2702 `Password: viper.GetString("download_client.torrent.qbittorrent.password")`; not masked by MaskSecrets (update_service.go:82-101).
- L3348 — - [ ] `DownloadClient.Usenet.Sabnzbd.APIKey` (`config.go:188`) — evidence: internal/config/config.go:2711 `APIKey: viper.GetString("download_client.usenet.sabnzbd.api_key")`; not masked by MaskSecrets (update_service.go:82-101).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "`OAuthGithubClientSecret` and `OAuthGoogleClientSecret" TODO.md   # the source item still exists (line numbers drift)
  test -e internal/config/config.go   # anchor file from the reconciliation evidence
  test -e internal/config/update_service.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_config_344.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving any new guard/repair path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/config/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_config_344.md`.

## Commit message

```
fix(config): Mask the remaining secrets returned by `GET /api/v1/config` (TODO.md:3344)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
