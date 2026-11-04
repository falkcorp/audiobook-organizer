<!-- file: docs/agent-tasks/platform-core/PC-17-05-system-backup-update-ui-generated-client.md -->
<!-- version: 1.0.0 -->
<!-- guid: bd0a4949-0031-48ec-9194-d94259951119 -->
<!-- last-edited: 2026-11-04 -->

# PC-17-05 — System, backup and update UI calls move to the generated client

| Field | Value |
|---|---|
| ID | PC-17-05 |
| Title | System, backup and update UI calls move to the generated client |
| Tier | **T1 — Sonnet / medium.** Frontend call-site swaps for routes that already exist, with a fixed list of call sites and a fixed rule for each. |
| Wave | W17 (stage S8, release `S8`) — runs **after** the S8 flip |
| Depends on | PC-17-04 |
| Trailer | `Stage-Release: S8 1` |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §1 (cells a/b), §7 S8 ("Cells a/b for the moved system/backup/update UI calls") |
| Owning spec | 07 §7.19 step 6; 07 §7.8 (the generated client, `web/src/api/core.ts`); 09 S8 "Done when" (cells a/b); 09 §9.3 "Rollback rehearsal" |

**Gate.** Merges only after the `S7` soak row exists and PC-17-04 has merged.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-17-05" -b feat/pc-17-05-s8-ui-calls origin/main
cd "$REPO/../abo-pc-17-05"
git worktree list
make web-deps
```

## Goal

Replace the hand-written `/api/v1/backup/*` and `/api/v1/update/*` calls in `web/src` with calls through ABO's
generated core client (`web/src/api/core.ts`, created at UI-0), **keeping every exported function's name, parameters
and return type unchanged**, so no page or component changes behaviour. A `/api/v1/system/*` or `/api/v1/health` call
moves only if the rule in step 3 says so.

## Files

Modify only:
- `web/src/services/api.ts`
- `web/src/components/settings/AutoUpdateSection.tsx` (only if step 5 requires it)
- `web/src/pages/Settings.tsx` (only if step 5 requires it)
- `web/src/components/SettingsGeneral.tsx` (the backup download link; see Planner notes)
- the matching `*.test.ts(x)` files next to each changed file

## Re-verify before editing

Run at your base. Expected values are at `748fd787a` (unchanged at this plan's HEAD) unless marked. A different count:
STOP and report; do not guess.

```bash
grep -c '^shipped_stage: S8$' ci/stage.txt                                  # 1 (PC-17-04 merged)
test -f web/src/api/core.ts && echo ok                                       # ok (UI-0's generated core client)
grep -c '"/api/core/v1/backups' core/openapi/core.json                       # >= 1
grep -c '"/api/core/v1/updater' core/openapi/core.json                       # >= 1
grep -n 'export async function \(createBackup\|listBackups\|restoreBackup\|deleteBackup\)(' web/src/services/api.ts   # 4 (748fd787a: lines 4657, 4670, 4679, 4692)
grep -n 'export async function \(getUpdateStatus\|checkForUpdate\|applyUpdate\)(' web/src/services/api.ts            # 3 (5878, 5887, 5896)
grep -n 'API_BASE}/backup/\|API_BASE}/update/' web/src/services/api.ts | wc -l   # 7
grep -n 'href={`/api/v1/backup/' web/src/components/SettingsGeneral.tsx       # 1 (:804)
grep -n "apiFetch('/api/v1/system/announcements')" web/src/components/AnnouncementBanner.tsx   # 1 (:43)
grep -n 'API_BASE}/system/' web/src/services/api.ts | wc -l                  # 3 (status :3243, storage :3252, factory-reset :3261)
grep -n "apiFetch('/api/v1/health')" web/src/App.tsx                         # 1 (:165)
grep -rn "from '.*api/core'" web/src --include=*.ts --include=*.tsx | grep -v test | head -3   # >= 1: an earlier switched call (S4-S6) to copy the pattern from
```

## Steps

1. **Find the copy-from pattern.** Open one function an earlier stage already switched to the generated client (the
   last grep above). Copy exactly how it obtains the client, calls the operation, unwraps `data`/`error`, and throws
   (`buildApiError` or the runtime's `Problem`). Use the same pattern everywhere below.
2. **Build the call table.** For each of the 7 `api.ts` functions above and the `SettingsGeneral.tsx` download link,
   find its row in `docs/compat-surfaces.md` (the legacy path appears as an `alias` row added by PC-17-04). Write a
   table in the PR description: legacy method + path → canonical method + path → generated operation. Expected
   canonical routes (05-backup §5B.3.8, 05-updater §5U.10):

   | Function | Legacy | Canonical |
   |---|---|---|
   | `createBackup` | `POST /api/v1/backup/create` | `POST /api/core/v1/backups` |
   | `listBackups` | `GET /api/v1/backup/list` | `GET /api/core/v1/backups` |
   | `restoreBackup` | `POST /api/v1/backup/restore` | `POST /api/core/v1/backups/{id}/restore` |
   | `deleteBackup` | `DELETE /api/v1/backup/{filename}` | `DELETE /api/core/v1/backups/{id}` |
   | download link | `GET /api/v1/backup/{filename}` | `GET /api/core/v1/backups/{id}/download` |
   | `getUpdateStatus` | `GET /api/v1/update/status` | `GET /api/core/v1/updater` |
   | `checkForUpdate` | `POST /api/v1/update/check` | `POST /api/core/v1/updater/check` |
   | `applyUpdate` | `POST /api/v1/update/apply` | `POST /api/core/v1/updater/apply` |

   If a row is missing from `docs/compat-surfaces.md`, or its canonical route differs from this table: STOP and report.
3. **The rule for any other call** (`/system/*`, `/health`, announcements): it moves **only** if
   `docs/compat-surfaces.md` has an `alias` row for its exact legacy path **and** the canonical route's
   `x-mediacore-permission` in `core/openapi/core.json` is the same permission class the legacy route had. Otherwise
   leave it unchanged and list it in the PR description as "not moved". Expected result at S8: `getSystemStatus`
   stays (a books route, 05-health §5H.10), `factory-reset` stays as it is (it moved at S4 r2), and both
   `/api/v1/health` callers stay (`App.tsx:165` reconnect probe, `getAppVersion`): they must work for every
   principal, and `GET /api/core/v1/health` needs `core:system.manage`.
4. **Rewrite each function body** with the generated operation. Keep the function's signature and return type. Map
   the native response onto the existing return type field by field: read the server-side alias that serves the
   legacy shape for that path (`git grep -n '<legacy path>' -- core internal | grep -i alias`) and copy its field
   mapping into a small mapper function next to the call (`toBackupInfo`, `toUpdateInfo`: names coined here). Do not
   invent a field.
5. **Values the legacy call never sent.** The native routes take an archive `id` (restore, delete, download) and an
   update `version` (apply). Obtain each exactly as the server-side alias does for the legacy call (read the alias code
   from step 4). If that needs the list item's `id`, add `id` to the `BackupInfo` mapper output and pass it from the
   caller in `web/src/pages/Settings.tsx` (the delete dialog uses `deleteBackupTarget`), changing nothing else. If the
   alias's rule cannot be reproduced without a new UI input: STOP and report.
6. Update the tests beside each changed file: mock the generated client operation instead of the `fetch` URL; assert
   the same UI behaviour as before. Do not delete a test case.
7. Run the checks below.

## Code contract

Unchanged exported signatures (from `web/src/services/api.ts` at `748fd787a`):

```ts
export async function createBackup(maxBackups?: number): Promise<BackupInfo>
export async function listBackups(): Promise<BackupListResponse>
export async function restoreBackup(filename: string, verify = true): Promise<{ message: string }>
export async function deleteBackup(filename: string): Promise<void>
export async function getUpdateStatus(): Promise<UpdateInfo>
export async function checkForUpdate(): Promise<UpdateInfo>
export async function applyUpdate(): Promise<void>
```

If step 5 forces a new parameter (for example `deleteBackup(filename, id)`), add it **last** and update every caller
(`grep -rn 'deleteBackup(' web/src`).

## Tests

- Existing vitest suites for `api.ts`, `Settings.tsx`, `SettingsGeneral.tsx`, `AutoUpdateSection.tsx` pass with the
  generated client mocked.
- R-test cells a/b for the moved calls: run `make compat-rehearsal` locally (CI skips it on a PR that does not change
  `ci/stage.txt`) and paste the cell a/b lines into the PR description. Cell (a): the S7 UI bundle against this build;
  cell (b): this bundle against the S7 build (the stale-build rule offers a reload).

## Acceptance

```bash
grep -n 'API_BASE}/backup/\|API_BASE}/update/' web/src/services/api.ts | wc -l          # 0
grep -n 'href={`/api/v1/backup/' web/src/components/SettingsGeneral.tsx | wc -l          # 0
grep -n "apiFetch('/api/v1/health')" web/src/App.tsx | wc -l                             # 1 (stays)
npm --prefix web run typecheck                                                           # exit 0
npm --prefix web test -- --run                                                           # all suites pass
make compat-rehearsal                                                                    # exit 0; cells a and b pass
make ci                                                                                  # exit 0
```

## Out of scope

Any other `api.ts` section (PC-18-04 moves the rest at S9); the shell (S9); changing what the Settings page shows;
server code.

## Rollback

Revert the PR. The aliases still serve the legacy paths throughout the window, so the old bundle works on S8.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Bump the versioned header of every changed file; one headerless `changelog.d/` fragment.
- Conventional commit, e.g. `refactor(web): backup and update calls use the generated core client`, with
  `Stage-Release: S8 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Report with exact counts (calls moved / calls left) and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- The row lists `web/src/pages/` and `AutoUpdateSection.tsx` but not `web/src/components/SettingsGeneral.tsx`
  (the backup download link, `SettingsGeneral.tsx:804` at `748fd787a`) or `web/src/components/AnnouncementBanner.tsx`
  (a `/api/v1/system/announcements` caller, moved only under step 3's rule).
- Whether `/api/v1/health` and `/api/health` keep HEAD's unauthenticated reach as aliases is PC-17-04's open point;
  this brief leaves both callers unchanged either way.
