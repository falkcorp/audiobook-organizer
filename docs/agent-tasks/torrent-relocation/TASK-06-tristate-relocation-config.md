<!-- file: docs/agent-tasks/torrent-relocation/TASK-06-tristate-relocation-config.md -->
<!-- version: 1.0.0 -->
<!-- guid: 14ccffe9-3e4e-4410-ac76-0d1af6bafa1f -->
<!-- last-edited: 2026-07-10 -->

# TASK-06 — Tri-state relocation config + Settings UI + docs (INIT-5 T6)

**Gate:** SPEC -> EXECUTE with a hard human gate: T2 is a REAL-DELUGE SPIKE with STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may start. The new relocation mode is config-gated (tri-state: off / physical-move / re-point-only); defaults STAY on today's behavior until the T2 spike is human-approved.
**File-ownership:** none cross-initiative. Within INIT-5: sole owner of `internal/config/config.go` (TASK-05 adds NO config key; at most it later adds an additive `Transmission` connection field inside the existing `TorrentClientConfig`, long after this task merges). Runs parallel to TASK-01 (disjoint files).

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · config-plumbing subagent · **Why:** backend field + resolver is mechanical, but the fallback semantics ARE the safety property of the whole initiative and the task spans Go + React · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/torrent-relocation-tristate-relocation-config" -b agent/torrent-relocation-tristate-relocation-config origin/main
cd "$REPO/.worktrees/torrent-relocation-tristate-relocation-config"
git rebase origin/main
```

## Goal

Add the tri-state relocation mode to config exactly as specified in the "Spec §Data model
(inlined, normative)" code block in Background below — that block is the complete, authoritative
source; no external document is needed. (Its origin,
`docs/specs/2026-07-10-torrent-relocation-design.md`, lives only in the separate PLANNING repo
`audiobook-organizer-plan-remaining-work` and is NOT present in the worktree you just created —
do not go looking for it.) Summary: a `RelocationMode` string type
with constants `RelocationOff` / `RelocationPhysicalMove` / `RelocationRePointOnly`, a config
field `TorrentRelocationMode string json:"torrent_relocation_mode"`, and a resolver
`func (c *Config) TorrentRelocation() RelocationMode` whose empty/unknown fallback derives from
the EXISTING `DelugeMoveEnabled` (true→physical-move, false→off). **Defaults STAY on today's
behavior** — this task must be a no-op for every existing install. Plus a Settings-UI tri-state
selector and a short docs page.

## Background (verify before editing)

- Legacy field: `DelugeMoveEnabled bool json:"deluge_move_enabled"` at
  `internal/config/config.go:498` ("enable MoveStorage calls when books are reorganized").
  It is KEPT and honored as the fallback — do not remove, rename, or change its default. DO add
  a deprecation line to its doc comment: `Deprecated: superseded by TorrentRelocationMode;
  honored as fallback only, slated for removal in a future cleanup once TorrentRelocationMode is
  the sole knob.` (spec Decision 4 — the dual-field state must be visibly intentional and
  time-boxed, not permanent).
- Consumers of the mode arrive in TASK-03; this task ships the field + resolver + UI only.
  Nothing else reads `TorrentRelocationMode` yet.
- **Spec §Data model (inlined, normative — copy this verbatim; no improvisation on names or
  JSON tags):**

  ```go
  // internal/config/config.go — NEW (T6). Placed next to DelugeMoveEnabled
  // (grep -n 'DelugeMoveEnabled' internal/config/config.go).

  // RelocationMode selects what the torrent client is asked to do after the
  // app relocates a book file that a torrent is seeding.
  type RelocationMode string

  const (
  	// RelocationOff: do not contact the torrent client (today's
  	// DelugeMoveEnabled=false behavior — the torrent keeps its old path).
  	RelocationOff RelocationMode = "off"
  	// RelocationPhysicalMove: legacy behavior — ask the client to physically
  	// move data (core.move_storage). Today's DelugeMoveEnabled=true.
  	RelocationPhysicalMove RelocationMode = "physical-move"
  	// RelocationRePointOnly: the app moved the data; the client only updates
  	// its recorded storage path (UpdateStoragePath). NEW.
  	RelocationRePointOnly RelocationMode = "re-point-only"
  )

  // Config field (string in JSON for forward compat):
  //   TorrentRelocationMode string `json:"torrent_relocation_mode"` // "", "off", "physical-move", "re-point-only"

  // TorrentRelocation resolves the effective mode. Empty/unknown values fall
  // back to the DelugeMoveEnabled-derived legacy mapping — defaults STAY on
  // today's behavior until the T2 spike is human-approved.
  func (c *Config) TorrentRelocation() RelocationMode {
  	switch RelocationMode(c.TorrentRelocationMode) {
  	case RelocationOff, RelocationPhysicalMove, RelocationRePointOnly:
  		return RelocationMode(c.TorrentRelocationMode)
  	}
  	if c.DelugeMoveEnabled {
  		return RelocationPhysicalMove
  	}
  	return RelocationOff
  }
  ```
- Frontend reality (verified 2026-07-10 at HEAD `fce58498`): the legacy `deluge_move_enabled`
  flag has **NO frontend surface under any name** — `grep -rn 'deluge_move_enabled' web/src`
  returns 0 hits, and `DelugeSettingsTab.tsx` is status/test/import only (no config form). You
  are CREATING the first relocation setting in the UI, not extending an existing field. The
  settings plumbing to hook into (all anchors verified — see the Re-verify block):
  - Payload type: `export interface Config` in `web/src/services/api.ts` (~:747); its
    `// Deluge integration` block already holds `protected_paths?: string[];` (~:844) — add
    `torrent_relocation_mode?: string;` there.
  - Page state: `export interface SettingsState` in `web/src/pages/Settings.tsx` (~:111),
    defaults in `initialSettings` (~:227), backend→frontend load mapping in the
    `nextSettings: SettingsState = { ... }` block (~:475, e.g.
    `organizationStrategy: config.organization_strategy || 'auto'` at ~:478).
  - Save path: `handleSave` in `web/src/hooks/useSettingsHandlers.ts` builds
    `updates: Partial<api.Config>` (~:434; e.g.
    `organization_strategy: settings.organizationStrategy` at ~:438).
  - Selector control pattern to mirror: the `organizationStrategy` `TextField select` +
    `MenuItem` block in `web/src/components/SettingsGeneral.tsx:258-277`
    (`props.handleChange('organizationStrategy', e.target.value)`).
  - Where to render: `web/src/components/settings/PathsSettingsTab.tsx`, directly above its
    `{/* Deluge Settings */}` section (~:264) — that component already receives `settings` +
    `handleChange` props.
  Selector options (4 — empty string is a real, explicit choice so no legacy-flag derivation is
  needed client-side): `""` → "Default (legacy — derived from the server's Deluge move flag)";
  `"off"` → "Off (leave torrent at old path)"; `"physical-move"` → "Physical move (legacy —
  client moves data)"; `"re-point-only"` → "Re-point only (app moves data; client updates its
  path)". No new UI framework pieces.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'DelugeMoveEnabled' internal/config/config.go        # legacy field, ~:498, >=1 hit
  grep -rn 'deluge_move_enabled' web/src                       # MUST be 0 hits — no frontend surface exists; you create the first (see Background)
  grep -n 'export interface Config {' web/src/services/api.ts  # payload type to extend, ~:747, 1 hit
  grep -n 'protected_paths' web/src/services/api.ts            # Deluge block inside Config, ~:844, >=1 hit
  grep -n 'export interface SettingsState' web/src/pages/Settings.tsx  # page state type, ~:111, 1 hit
  grep -n 'organizationStrategy: config.organization_strategy' web/src/pages/Settings.tsx  # load-mapping line to mirror, ~:478, 1 hit
  grep -n 'organization_strategy: settings.organizationStrategy' web/src/hooks/useSettingsHandlers.ts  # save payload to extend, ~:438, 1 hit
  grep -n "handleChange('organizationStrategy'" web/src/components/SettingsGeneral.tsx  # select pattern to mirror, ~:262, 1 hit
  grep -n 'Deluge Settings' web/src/components/settings/PathsSettingsTab.tsx  # render location, ~:264, 1 hit
  grep -n 'TorrentRelocationMode' internal/config/config.go    # must be 0 hits before you start (see Idempotency)
  ```
  Zero hits on the first grep = STOP and report; do not guess. Unexpected HITS on the
  `deluge_move_enabled` web/src grep = a frontend surface appeared since 2026-07-10 — extend it
  instead of creating a parallel one, and report the deviation.

## Step-by-step

1. In `internal/config/config.go`, add (next to `DelugeMoveEnabled`) the `RelocationMode` type,
   the three constants, the `TorrentRelocationMode` field, and the `TorrentRelocation()`
   resolver — exactly per the "Spec §Data model (inlined, normative)" code block in Background —
   plus the `Deprecated:` line on `DelugeMoveEnabled`'s
   doc comment (Background). Edge semantics (state them in code comments too):
   empty string OR any unknown value ⇒ fall back to the `DelugeMoveEnabled` mapping — unknown is
   never an error and never silently becomes re-point-only.
2. Purely additive — do not touch other config fields, defaults blocks, or validation beyond
   what the new field needs. Do not change any signature.
3. Tests in `internal/config/config_test.go` (`TestTorrentRelocationModeFallback`):
   unset + DelugeMoveEnabled=true → `physical-move`; unset + false → `off`; each explicit value
   wins over the legacy flag; unknown value ("banana") + true → `physical-move` (fallback, not
   error). Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added).
4. Frontend (four small edits — full wiring recipe, anchors in the Re-verify block):
   (a) `web/src/services/api.ts`: add `torrent_relocation_mode?: string;` inside
   `export interface Config` next to `protected_paths?: string[];` (the `// Deluge integration`
   block). (b) `web/src/pages/Settings.tsx`: add `torrentRelocationMode: string;` to
   `export interface SettingsState`, default `torrentRelocationMode: '',` in `initialSettings`,
   and `torrentRelocationMode: config.torrent_relocation_mode ?? '',` in the `nextSettings`
   load-mapping block (beside the `organizationStrategy` line). (c)
   `web/src/hooks/useSettingsHandlers.ts`: add
   `torrent_relocation_mode: settings.torrentRelocationMode,` to the `updates: Partial<api.Config>`
   object in `handleSave` (beside `organization_strategy:`). (d)
   `web/src/components/settings/PathsSettingsTab.tsx`: render the selector directly above the
   `{/* Deluge Settings */}` section using the component's existing `settings` + `handleChange`
   props, mirroring the `TextField select` + `MenuItem` pattern of
   `web/src/components/SettingsGeneral.tsx:258-277`
   (`handleChange('torrentRelocationMode', e.target.value)`); the 4 options (incl. the explicit
   `""` "Default (legacy)" option) are listed in Background.
5. Docs: NEW `docs/torrent-relocation-modes.md` (4-line header, fresh guid) — one page: the
   three modes, the legacy fallback mapping, and that re-point-only requires the TASK-02
   spike-approved Deluge mechanism.
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. (`make ci` includes the frontend build via the standard pipeline; if the UI change needs interactive verification, `make web-dev`.)

## Acceptance criteria

- [ ] `grep -n "TorrentRelocationMode" internal/config/config.go` hits (field) and `grep -n "func (c \*Config) TorrentRelocation()" internal/config/config.go` hits (resolver)
- [ ] `TestTorrentRelocationModeFallback` green and covers: unset→legacy mapping both ways, explicit-wins, unknown→legacy (4 cases minimum)
- [ ] `grep -n 'Deprecated:' internal/config/config.go | grep -in deluge` hits (DelugeMoveEnabled deprecation note present)
- [ ] `grep -rn "torrent_relocation_mode" web/src` hits (UI wired to the JSON key)
- [ ] `grep -rn "TorrentRelocation()" internal/ --include='*.go' | grep -v 'config\|_test'` → 0 hits (no consumer wired yet — that is TASK-03)
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean (`make ci` exits 0, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file; new docs file has a fresh guid.

## Commit message

```
feat(config): tri-state torrent relocation mode (off/physical-move/re-point-only) (INIT-5 T6)

Adds TorrentRelocationMode with a resolver whose empty/unknown fallback derives
from the existing DelugeMoveEnabled flag, so every existing install keeps
today's behavior bit-for-bit. Settings UI tri-state selector + docs page. No
pipeline consumes the mode until the call-site migration (TASK-03), which is
gated on the TASK-02 real-Deluge spike sign-off.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/torrent-relocation-tristate-relocation-config
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "TorrentRelocationMode" internal/config/config.go` hits, this task is already
applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the
field disappears and every install resolves relocation exactly as before via `DelugeMoveEnabled`;
no data migration in either direction (unknown JSON keys are ignored on load).
