<!-- file: docs/agent-tasks/todo-completion/web/TASK-158-add-a-settings-panel-section-to-edit-path-aliase.md -->
<!-- version: 1.0.0 -->
<!-- guid: 34cb9557-8311-4bf6-a175-96f02fa9d536 -->
<!-- last-edited: 2026-08-21 -->

# TASK-158 — Add a Settings panel section to edit path_aliases (2026-08-20-dual-path-settings-panel.md#1)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Why:** New component + multi-file state wiring (Settings.tsx state, useSettingsHandlers.ts payload, new test file); mechanical but touches 4 files that must stay consistent. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 0 as of commit 46628240 (later edits shift lines) — re-find it with `this item lives in a todo.d/ fragment (see src_id), not in TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-16.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-158-add-a-settings-panel-section-to-edit-path-aliase" -b agent/web-158-add-a-settings-panel-section-to-edit-path-aliase origin/main
cd "$REPO/.worktrees/web-158-add-a-settings-panel-section-to-edit-path-aliase"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add an editable list UI for config.path_aliases (root / windows / unc / smb_url per row, add/remove rows) inside the existing Path Settings tab, following the EmbeddingSettingsSection controlled-component pattern (separate state slot + onChange(patch) callback, not folded into the giant flat `settings` object), and wire it into the existing PATCH /config save flow via updateConfig().

## Background (verify before editing)

- Config.path_aliases: PathAlias[] already exists on the wire type (web/src/services/api.ts:842-852) and is consumed read-only by PathLinks.tsx/usePathAliases -- there is no writer anywhere in web/.
- docs/design/2026-08-20-dual-path-display.md:474 explicitly deferred a Settings UI as v1 open question 1: 'v1 is config/env only. An editable Settings panel is deferred unless you want it in scope now.'
- Settings.tsx already has a tab (index 4, PathsSettingsTab) dedicated to path-related settings, and a 'Protected Paths' Paper section inside it (web/src/components/settings/PathsSettingsTab.tsx:216-256) shows the in-tab layout convention (Paper + Typography + form controls) to match visually.
- The EmbeddingSettingsSection pattern is the cleanest precedent for a config sub-object with its own save wiring: Settings.tsx holds `embeddingConfig` state (line 363), loads it from `config.embedding` on fetch (line 601: `if (config.embedding) setEmbeddingConfig(config.embedding)`), and useSettingsHandlers.ts spreads it into the PATCH body as `embedding: embeddingConfig` (line 491).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "export interface PathAlias" web/src/services/api.ts   # 1 hit L842 — PathAlias{root,windows,unc,smb_url} is the client-side type to edit
  grep -n "path_aliases" web/src/services/api.ts   # hit at L852 (interface field) — Config.path_aliases is the field this panel must read/write
  grep -n "PathsSettingsTab" web/src/pages/Settings.tsx   # hits L36 (import), L878 (render) — PathsSettingsTab is already mounted as Settings tab index 4, the natural host tab
  grep -n "embeddingConfig" web/src/pages/Settings.tsx   # hits L363 (useState), L731 (destructure), L859 (render) — EmbeddingSettingsSection is the precedent pattern: separate `config`/`onChange(patch)` props, its own state slot in Settings.tsx, wired into the save payload
  grep -n "embedding: embeddingConfig" web/src/hooks/useSettingsHandlers.ts   # 1 hit L491 — useSettingsHandlers.ts spreads embeddingConfig into the PATCH payload under its own top-level config key
  grep -n "export async function updateConfig" web/src/services/api.ts   # 1 hit L2277 — updateConfig(Partial<Config>) is the existing save call to reuse, no new endpoint needed
  grep -n "handleEmbeddingChange" web/src/hooks/useSettingsHandlers.ts   # hits L130 (type), L296 (impl), L916 (return) — handleEmbeddingChange(patch) is the exact onChange-wiring precedent to copy for a new handlePathAliasesChange
  ```

### Reuse — don't invent

- Use `EmbeddingSettingsSection.tsx (config/onChange(patch) props pattern)` in `web/src/components/settings/EmbeddingSettingsSection.tsx` (verify: `grep -n "onChange: (patch: Partial<api.EmbeddingConfig>) => void" web/src/components/settings/EmbeddingSettingsSection.tsx`) — do NOT write a parallel helper.
- Use `settings.metadataSources add/remove-row list pattern` in `web/src/components/settings/MetadataSettingsTab.tsx` (verify: `grep -n "settings.metadataSources.map" web/src/components/settings/MetadataSettingsTab.tsx`) — do NOT write a parallel helper.
- Use `updateConfig()/getConfig() API client` in `web/src/services/api.ts` (verify: `grep -n "export async function getConfig" web/src/services/api.ts`) — do NOT write a parallel helper.

## Step-by-step

1. Create web/src/components/settings/PathAliasesSection.tsx exporting `PathAliasesSection({ aliases, onChange }: { aliases: api.PathAlias[]; onChange: (aliases: api.PathAlias[]) => void })`. Render a MUI Paper with a heading 'Path Aliases', an explanatory Alert (mirror the tone of PathsSettingsTab.tsx:98-103), and one row per alias with 4 TextFields (Root, Windows, UNC, SMB URL) plus a delete IconButton, plus an 'Add Alias' Button that appends `{ root: '', windows: '', unc: '', smb_url: '' }`. Every edit calls onChange with a new array (immutable update), mirroring the map/index-based update style at web/src/components/settings/MetadataSettingsTab.tsx:227 (settings.metadataSources.map).
2. In web/src/pages/Settings.tsx: add `const [pathAliases, setPathAliases] = useState<api.PathAlias[]>([]);` near the embeddingConfig useState at line 363. In the config-load effect where `if (config.embedding) setEmbeddingConfig(config.embedding);` appears (line 601), add a sibling line `if (config.path_aliases) setPathAliases(config.path_aliases);`.
3. In web/src/pages/Settings.tsx, inside the PathsSettingsTab tab panel (around line 878-890, right after the closing `/>` of PathsSettingsTab), render `<PathAliasesSection aliases={pathAliases} onChange={handlePathAliasesChange} />` (or pass `pathAliases`/`setPathAliases` as props into PathsSettingsTab and render it there instead, matching how DelugeSettingsTab is nested inside PathsSettingsTab at PathsSettingsTab.tsx:260 -- prefer this nesting so the new section lives next to the other path-related settings).
4. In web/src/hooks/useSettingsHandlers.ts: add `pathAliases: api.PathAlias[];` to the params interface near line 110 (`embeddingConfig: api.EmbeddingConfig;`), destructure it in the params list near line 245, add a `handlePathAliasesChange` matching the shape of `handleEmbeddingChange` at line 296 (but since PathAliasesSection.tsx will already own array add/remove/edit logic and just call onChange with the full next array, `handlePathAliasesChange` can simply be `(next: api.PathAlias[]) => setPathAliases(next)` -- add the corresponding `setPathAliases` param too), and add `path_aliases: pathAliases,` to the `updates` object near line 491 (sibling to `embedding: embeddingConfig,`). Add `handlePathAliasesChange` to the hook's return object near line 916.
5. Back in Settings.tsx, pass `pathAliases`, `setPathAliases` into the useSettingsHandlers() call args (matching how embeddingConfig/setEmbeddingConfig are passed) and destructure `handlePathAliasesChange` from its return value.
6. Create web/src/components/settings/PathAliasesSection.test.tsx (mirror web/src/components/settings/EmbeddingSettingsSection.test.tsx if present, else DedupSettingsSection.test.tsx for structure): render with an initial `aliases` array, assert each field renders its value, type into the Windows field of the first row and assert onChange was called with an array where only that row's `windows` changed (siblings untouched), click 'Add Alias' and assert onChange was called with an appended empty row, click the delete icon on a row and assert onChange was called with that row removed and others in original order.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_158.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- config.path_aliases is null/undefined on a fresh install (SeedPathAliases may not have run yet, or DB has no config row) -- must render an empty list with just the 'Add Alias' button, not throw.
- A row with all-empty fields ('' root) should still be sendable (server-side validation, not client-side, decides whether an empty root is rejected) -- do not silently drop empty rows on save, since that would surprise a user who just clicked Add and hasn't filled it in yet.
- Windows/UNC/SMBURL are individually optional per the PathAlias Go struct (json tags have no omitempty on Root only) -- do not require all 4 fields to be non-empty before allowing Add/Save.

## Tests

- web/src/components/settings/PathAliasesSection.test.tsx 'renders one row per configured alias with root/windows/unc/smb_url values' -- pass a 2-element aliases array, assert 2 sets of 4 TextFields with matching values.
- web/src/components/settings/PathAliasesSection.test.tsx 'editing one field in one row calls onChange with only that field changed' -- type into row 0's Windows field, assert the onChange payload leaves row 1 byte-identical to the input.
- web/src/components/settings/PathAliasesSection.test.tsx 'Add Alias appends an empty row without touching existing rows' -- anti-over-suppression check that add doesn't clear or reorder existing aliases.
- web/src/components/settings/PathAliasesSection.test.tsx 'delete removes only the targeted row' -- 3-row input, delete row index 1, assert onChange payload is [row0, row2] in that order.

Anti-over-suppression test: `PathAliasesSection.test.tsx 'Add Alias appends an empty row without touching existing rows'` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] npm --prefix web run lint passes with no new errors.
- [ ] npm --prefix web test -- PathAliasesSection passes all new tests.
- [ ] grep -n "path_aliases: pathAliases" web/src/hooks/useSettingsHandlers.ts returns 1 hit, proving the panel's state reaches the PATCH /config payload.
- [ ] Manual smoke (optional, not required for CI): load Settings, Paths tab shows the new Path Aliases section seeded from GET /config path_aliases, edit a row, Save, reload page, edited value persists.
- [ ] Anti-over-suppression test: `PathAliasesSection.test.tsx 'Add Alias appends an empty row without touching existing rows'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_158.md`.

## Commit message

```
feat(web): Add a Settings panel section to edit path_aliases (2026-08-20-dual-path-settings-panel.md#1)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`npm --prefix web run lint passes with no new errors.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Server-side PATCH /config already accepts path_aliases (internal/config/persistence.go:904-905 calls SeedPathAliases/ValidatePathAliases on load) -- confirm during implementation whether the config PATCH handler round-trips path_aliases unchanged (it should, since Config is decoded generically), but this was not independently verified by this scout pass; if the PATCH handler drops unknown/optional fields, that's a small additional backend fix, not a redesign.
