<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-11-backend-mode-toggle-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0628f4f5-5e44-4212-9bf4-63f7c7e4cdb0 -->
<!-- last-edited: 2026-07-03 -->

# TASK-11 — Backend-mode toggle frontend (settings selector + model-download prompt)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet · frontend/webui subagent · **Wave:** 4 · **Depends on:** TASK-10 (backend-mode toggle, backend: config shape, `register.go` selector, `GET/POST /api/v1/ai/backends/*` endpoints)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-11-backend-mode-toggle-frontend" -b agent/cr-11-backend-mode-toggle-frontend origin/main
cd "$REPO/.worktrees/cr-11-backend-mode-toggle-frontend"
git rebase origin/main
```

**Before writing any frontend code, confirm TASK-10 has actually merged to
`main` and rebase onto it** — this task is pure UI/wiring on top of the
config fields and endpoints TASK-10 introduces. If `internal/ai` has no
`BackendMode`/`AIBackendConfig` symbols yet (see verification grep below),
STOP: TASK-10 has not landed, and this brief's endpoint paths and field names
are the *design intent*, not verified fact — do not guess a shape and ship a
UI against it.

```bash
grep -rn "AIBackendConfig\|BackendMode\|ai/backends" internal/ web/src --include="*.go" --include="*.ts" --include="*.tsx"
```

If that grep is empty, this task is blocked — comment on the tracking issue
and stop here rather than inventing a config shape.

## Goal

Give operators a Settings UI to choose the embeddings/LLM backend mode
(`disabled | openai | local | openai-fallback-local`, per TASK-10's config),
enter local endpoint/model overrides when a local mode is selected, and — when
a local mode is selected but the target model isn't pulled into Ollama yet —
prompt to pull it with visible progress, instead of the feature silently
404'ing against Ollama. This closes the FE half of TOGGLE-1..6 /
MATCH-8 (`docs/consultancy/03-matching-and-backends.md`, Design section,
"FE" + "Model-download prompt" paragraphs).

## Background (verify before editing)

- Source finding (consultancy doc, Design: LLM/Embedding Backend-Mode Toggle,
  primary design): *"FE — 'AI Backends' card in Settings (extend
  EmbeddingSettingsSection/MetadataScoringSection): two mode selects, local
  endpoint/model fields, Test Connection → new `GET
  /api/v1/ai/backends/status` (probes endpoint, lists pulled models via
  `/api/tags`, reports effective mode + last fallback reason)."* and
  *"Model-download prompt — when local selected and status shows model
  absent: dialog 'qwen2.5:7b-instruct not pulled (4.7GB) — Pull now?' → `POST
  /api/v1/ai/backends/pull-model`, runs `ollama pull` through the existing
  managed external-tool lifecycle ... streaming progress as an op-registry
  operation; mode stays pending-unavailable until probe passes."*
- As of this writing (2026-07-03, before TASK-10), **none of this backend
  plumbing exists** — `grep -rn "AIBackendConfig\|BackendMode\|ai/backends"`
  across `internal/` and `web/src/` returns nothing. TASK-10 is expected to
  add: the `AIBackendConfig{EmbeddingMode, LLMMode, LocalBaseURL,
  LocalEmbeddingModel, LocalLLMModel}` struct to the config blob, a
  `register.go` backend selector, and the two endpoints named above. **This
  brief's endpoint paths/field names may drift once TASK-10 actually lands —
  re-verify against TASK-10's PR/diff, not just this doc, before wiring
  `apiFetch` calls.**
- Existing FE patterns to reuse (confirmed present in this worktree, re-verify
  the grep below):
  - `web/src/components/settings/EmbeddingSettingsSection.tsx` — the existing
    embedding settings card (`enabled` switch, `model`/`base_url` text
    fields, `vector_backend` select), driven by a `config`/`onChange(patch)`
    prop pair from `web/src/pages/Settings.tsx`. This is the sibling
    component to extend or place a new "AI Backends" card next to (per the
    design doc's "extend EmbeddingSettingsSection/MetadataScoringSection").
  - `web/src/components/settings/MetadataScoringSection.tsx` — same
    `config`/`onChange` pattern, LLM-side settings (`llm_enabled`, model
    fields).
  - `web/src/pages/Settings.tsx` tab index 3 (`TabPanel value={tabValue}
    index={3}`) composes `DedupSettingsSection`, `EmbeddingSettingsSection`,
    `MetadataScoringSection`, `ScheduledTasksSection` in sequence inside
    `<Box sx={{ mt: 4 }}>` wrappers — this is the "CFG-2" settings-UI layout
    convention (PR #1514) to follow for the new "AI Backends" card.
  - `web/src/services/api.ts` — `apiFetch` wrapper convention: every API call
    is `export async function xyz(...): Promise<T> { const response =
    await apiFetch(`${API_BASE}/...`, {...}); ... }`. `EmbeddingConfig`
    interface is defined near line 626 and referenced from the `Config`
    interface (`embedding?: EmbeddingConfig`).
  - `web/src/components/tools/ToolsPanel.tsx` — the existing
    "managed external-tool lifecycle" pull/install UI pattern referenced by
    the design doc: `getTools()` on mount, `installTool(name)` on click with
    a per-tool `installing` boolean map, re-fetching `getTools()` after
    install completes. This is a simple poll-after-action pattern (no SSE
    streaming) — use it as the baseline shape for the model-pull button;
    only add streaming/op-registry progress polling if TASK-10's
    `pull-model` endpoint actually returns an op ID to poll (verify this
    against TASK-10's implementation, don't assume).
  - `web/src/components/settings/ToolsSettingsTab.tsx` — shows the
    `useAdvancedSettings` hook pattern for gating advanced fields
    (`showAdvanced`), useful if the local base-url/model fields should be
    hidden by default.
  - TODO.md "🧰 Managed External-Tool Lifecycle" section (search
    `Managed External-Tool Lifecycle` in `TODO.md`) — TOOL-1..6, already
    shipped in PR #1465: `ToolRegistry`, `POST
    /api/v1/tools/:name/install`, `OllamaDaemon` PID-file lifecycle. The
    model-pull backend endpoint (TASK-10) is expected to reuse this
    lifecycle rather than shell out ad hoc — this FE task just needs to call
    whatever endpoint TASK-10 exposes and render its result/progress.

- **Re-verify these anchors before editing** — file contents may have
  changed since this brief was written:
  ```bash
  grep -n "interface EmbeddingSettingsSectionProps\|export function EmbeddingSettingsSection" web/src/components/settings/EmbeddingSettingsSection.tsx
  grep -n "export interface EmbeddingConfig" web/src/services/api.ts
  grep -n "TabPanel value={tabValue} index={3}" web/src/pages/Settings.tsx
  grep -n "export async function getTools\|export async function installTool\|export interface ToolStatus" web/src/services/api.ts
  grep -rn "AIBackendConfig\|BackendMode\|ai/backends/status\|ai/backends/pull-model" internal/ web/src
  ```
  The last grep is the gate: it must show TASK-10's symbols/endpoints before
  you proceed past step 1 below.

## Step-by-step

1. Confirm TASK-10 has landed (grep above). Note the **actual** field names
   of `AIBackendConfig` (or whatever it was named) and the **actual**
   request/response shape of the status/pull-model endpoints from TASK-10's
   Go code (`internal/api` or wherever the handlers live) — do not copy the
   field names in this brief verbatim without checking, since TASK-10 may
   have adjusted them during implementation.
2. In `web/src/services/api.ts`, add:
   - An `AIBackendConfig` (or matching TASK-10 name) TypeScript interface
     mirroring the Go struct, added to the `Config` interface the same way
     `embedding?: EmbeddingConfig` is (grep `embedding?: EmbeddingConfig` to
     find the exact spot).
   - `getAIBackendsStatus(): Promise<AIBackendsStatus>` — `GET
     ${API_BASE}/ai/backends/status`, following the `apiFetch` convention
     used by `getTools()`.
   - `pullAIBackendModel(model: string): Promise<...>` — `POST
     ${API_BASE}/ai/backends/pull-model`, mirroring `installTool()`'s
     `apiFetch(..., { method: 'POST', body: JSON.stringify({ name }) })`
     shape (check `installTool`'s actual body shape first).
3. Create `web/src/components/settings/AIBackendsSection.tsx` (new file,
   sibling to `EmbeddingSettingsSection.tsx`), following that file's
   `config`/`onChange(patch: Partial<AIBackendConfig>)` prop convention:
   - Two `TextField select` mode pickers (embedding mode, LLM mode), options
     = the four modes from TASK-10's config (`disabled | openai | local |
     openai-fallback-local` — confirm exact string values from the Go
     const/enum, don't assume spelling).
   - When either mode select is `local` or `openai-fallback-local`, reveal
     `LocalBaseURL` / `LocalEmbeddingModel` / `LocalLLMModel` text fields
     (conditionally rendered, same pattern as `showAdvanced` conditional
     blocks in `ToolsSettingsTab.tsx`).
   - A "Test Connection" button that calls `getAIBackendsStatus()` and
     displays effective mode + last fallback reason + per-model
     pulled/absent status in a small results panel (reuse MUI `Alert`/
     `Chip` components already imported elsewhere in `settings/` files —
     grep `from '@mui/material'` in `EmbeddingSettingsSection.tsx` for the
     import style).
   - When status reports a selected local model absent, render a
     confirmation dialog (MUI `Dialog`) — "`<model>` not pulled (`<size if
     known>`) — Pull now?" — Confirm calls `pullAIBackendModel(model)` and
     shows an "Installing…"/progress state modeled on
     `ToolsPanel.tsx`'s `installing` boolean map, re-calling
     `getAIBackendsStatus()` afterward to confirm the probe now passes.
4. Wire the new section into `web/src/pages/Settings.tsx` tab index 3,
   alongside `EmbeddingSettingsSection`/`MetadataScoringSection` (same
   `<Box sx={{ mt: 4 }}>` wrapper pattern), with its own
   `aiBackendConfig`/`handleAIBackendChange` state plumbing mirroring
   `embeddingConfig`/`handleEmbeddingChange` (grep those two names in
   `Settings.tsx` to find the exact `useState`/handler pattern to copy).
5. Add `web/src/components/settings/AIBackendsSection.test.tsx` mirroring
   `EmbeddingSettingsSection.test.tsx`'s structure: render with a default
   config, assert the mode selects render, assert `onChange` fires with the
   right patch on selection, assert local fields are hidden/shown based on
   mode, assert the pull-model dialog appears when a mocked
   `getAIBackendsStatus()` response reports the model absent.
6. Add (or extend, if `web/tests/e2e/settings-ai-persistence.spec.ts` already
   covers the AI backends card once TASK-10's fields exist) a Playwright
   spec under `web/tests/e2e/` exercising: switching to local mode reveals
   the local fields, saving persists them (reuse
   `settings-configuration.spec.ts`'s save/reload pattern — grep it for the
   exact save-and-reload assertion idiom), and the pull-model dialog
   flow with a mocked/absent-model status response.
7. Bump the file header (version bump + `last-edited`) on every file you
   touch, per `.standards/instructions/file-headers.md`.
8. Update `docs/reference/config-api-shape.md` with the new
   `AIBackendConfig` fields in the `GET /config` / `PUT /config` shape
   (follow that file's existing table/section format — do not replace the
   whole document, only add the new fields).

## How to test

```bash
cd web
npm run build
npx vitest run src/components/settings/AIBackendsSection.test.tsx src/components/settings/EmbeddingSettingsSection.test.tsx
npx eslint src/components/settings/AIBackendsSection.tsx src/pages/Settings.tsx src/services/api.ts
npx tsc --noEmit
```

If Playwright coverage was added:
```bash
cd web
npx playwright test tests/e2e/settings-ai-persistence.spec.ts
```

Then, from the repo root, confirm the embedded build still compiles (this
task should not touch Go code, but a broken frontend build breaks
`embed_frontend`):
```bash
go build ./...
go vet ./...
```

## Acceptance criteria

- [ ] TASK-10's `AIBackendConfig`/mode-enum/endpoint symbols were confirmed
      present in `main` before any FE code was written (grep output pasted
      into the PR description).
- [ ] New "AI Backends" settings card renders two mode selectors (embedding,
      LLM) with the four modes from TASK-10's config.
- [ ] Local base-url/model fields are shown only when a local-involving mode
      is selected for the corresponding subsystem.
- [ ] "Test Connection" calls the status endpoint and displays effective
      mode, last fallback reason, and per-model pulled/absent state.
- [ ] Selecting a local mode with an absent model surfaces a confirmation
      dialog; confirming calls the pull-model endpoint and shows progress,
      then re-probes status.
- [ ] New card is wired into `Settings.tsx` tab index 3 using the same
      `config`/`onChange` plumbing as `EmbeddingSettingsSection`.
- [ ] `docs/reference/config-api-shape.md` documents the new config fields.
- [ ] `npx vitest run` (new + existing settings tests) is green;
      `npx tsc --noEmit` and `npx eslint` are clean; `go build ./...` and
      `go vet ./...` remain clean (no Go files should need changes).
- [ ] File headers bumped on every changed/new file.

## Commit message

```
feat(settings): add AI backend-mode selector and model-pull prompt to Settings UI

TOGGLE-1..6/MATCH-8 (consultancy 03-matching-and-backends.md) call for FE
wiring on top of the backend-mode toggle: independent embedding/LLM mode
selects, local endpoint/model fields, a Test Connection status probe, and a
model-download confirmation dialog reusing the managed external-tool
lifecycle pull pattern. Adds the AI Backends settings card and wires it into
the existing CFG-2 Settings tab layout.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-11-backend-mode-toggle-frontend
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Before starting, run the verification grep from "START HERE" — if
`AIBackendConfig`/`BackendMode`/`ai/backends` symbols don't exist anywhere in
`internal/` or `web/src/`, TASK-10 has not landed and this task cannot
proceed; do not stub out a fake shape. If a card matching "AI Backends" (or
equivalent mode selectors + model-pull dialog) already exists in
`web/src/components/settings/`, this task is done — verify with `grep -rln
"pull-model\|AIBackendConfig" web/src/components/settings/` and confirm a
component renders both mode selects and the pull-model dialog described
above. Rollback = revert the commit; this task touches only frontend
files and `docs/reference/config-api-shape.md`, so revert is a pure UI
regression with no data-model or backend impact.
