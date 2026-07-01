<!-- file: docs/agent-tasks/library-ui/TASK-01-ollama-download-link.md -->
<!-- version: 1.0.0 -->
<!-- guid: db42ee14-f56c-4b9f-94ed-ee8bcad97a38 -->
<!-- last-edited: 2026-07-01 -->

# TASK-01 — "Download latest Ollama" deep-link on Settings embeddings section (EMB-UI-1)

**Priority:** P3 · **Effort:** XS · **Recommended subagent:** Haiku · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/lu-ollama-download-link" -b agent/lu-ollama-download-link origin/main
cd "$REPO/.worktrees/lu-ollama-download-link"
git rebase origin/main
```

## Goal

Add a visible deep-link to `https://ollama.com/download` on the Settings
embeddings section, so a user who doesn't already have Ollama installed can
get it in one click instead of having to know the URL. This is a trivial,
fully isolated UI addition — do not touch any other Settings section or any
backend code.

## Background (verify before editing)

- `web/src/components/settings/EmbeddingSettingsSection.tsx` is the component
  that renders the embeddings backend configuration in Settings. As of this
  writing, line 72 has a helper text that mentions Ollama but provides no
  link:
  ```
  helperText="Blank = OpenAI. Set to http://localhost:11434/v1 for Ollama."
  ```
- `web/src/pages/Settings.tsx` line 863 has a related helper text for the
  managed-binary directory ("Directory where managed binaries (Ollama,
  fpcalc) are downloaded") — this is a **different, unrelated** setting
  (managed-binary auto-download directory). Do NOT edit `Settings.tsx` for
  this task; it's provided only as background context that Ollama is
  referenced in two places in this codebase for two different purposes (an
  OpenAI-compatible endpoint URL vs. an auto-managed binary path).
- No `ollama.com/download` link exists anywhere in `web/src` today.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "Ollama\|ollama" web/src/components/settings/EmbeddingSettingsSection.tsx
  ```

## Step-by-step

1. Open `web/src/components/settings/EmbeddingSettingsSection.tsx` and locate
   the embeddings backend URL field (re-verify with the grep above — do not
   assume the line number from this brief).
2. Add a small, visible deep-link **above** the embeddings backend settings
   (before the backend-URL field / at the top of the section), using MUI's
   `Link` component (match the import style already used in sibling settings
   components — check `web/src/components/settings/` for the existing MUI
   import pattern), e.g.:
   ```tsx
   <Link href="https://ollama.com/download" target="_blank" rel="noopener noreferrer">
     Download the latest Ollama
   </Link>
   ```
   Wrap it in whatever layout element (`Box`, `Typography`, `Stack`) matches
   the existing spacing conventions in this file — do not introduce a new
   layout primitive if the file already has one in use nearby.
3. Do not modify the existing helper text on the backend-URL field; the new
   link is additive.
4. Do not touch `Settings.tsx` or any other file — this task is scoped to one
   file only.
5. Bump the file header on `EmbeddingSettingsSection.tsx` (version bump +
   `last-edited` date) per `.standards/instructions/file-headers.md`.

## How to test

```bash
cd web && npm install && npm run build && npm test
```

## Acceptance criteria

- [ ] A link to `https://ollama.com/download` is visible in the Settings
      embeddings section, opens in a new tab (`target="_blank"` with
      `rel="noopener noreferrer"`).
- [ ] The existing embeddings backend-URL field and its helper text are
      unchanged.
- [ ] No other file besides `EmbeddingSettingsSection.tsx` is modified.
- [ ] `npm run build` and `npm test` are green.
- [ ] A test (new or extended in the existing test file for this component,
      if one exists — check for
      `web/src/components/settings/EmbeddingSettingsSection.test.tsx`) asserts
      the link is rendered with the correct `href`.
- [ ] File header bumped on `EmbeddingSettingsSection.tsx`.

## Commit message

```
feat(settings): add Ollama download deep-link to embeddings section (EMB-UI-1)

Users configuring the OpenAI-compatible embeddings endpoint for Ollama had to
already know to visit ollama.com/download — there was no in-app link. Add a
visible deep-link above the embeddings backend settings.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/lu-ollama-download-link
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If a link to `ollama.com/download` already renders in the embeddings section,
this task is done — verify with
`grep -n "ollama.com/download" web/src/components/settings/EmbeddingSettingsSection.tsx`.
Rollback = revert the commit; no other component or backend behavior is
affected by this change.
