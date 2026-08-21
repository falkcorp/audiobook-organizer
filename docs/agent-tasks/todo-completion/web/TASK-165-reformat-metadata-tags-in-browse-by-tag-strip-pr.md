<!-- file: docs/agent-tasks/todo-completion/web/TASK-165-reformat-metadata-tags-in-browse-by-tag-strip-pr.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5caefd7b-0175-4b65-8925-f881823fca53 -->
<!-- last-edited: 2026-08-21 -->

# TASK-165 — Reformat metadata:* tags in Browse by Tag: strip prefix, 'key: value' spacing (TODO.md L1350)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Pure string-formatting change, but must handle the 3-segment case (metadata:language:en → 2 colons) correctly, not just strip the first prefix. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 1350 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🏷️ **\"Browse by Tag\" surfaces internal bookkeeping" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-165-reformat-metadata-tags-in-browse-by-tag-strip-pr" -b agent/web-165-reformat-metadata-tags-in-browse-by-tag-strip-pr origin/main
cd "$REPO/.worktrees/web-165-reformat-metadata-tags-in-browse-by-tag-strip-pr"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For any visible tag in the `metadata:` namespace (i.e. not already hidden by part 1's dedup:/metadata:source: rule), render its label as `key: value` instead of `metadata:key:value` — e.g. `metadata:language:en` → `language: en`. Only affects display text; the underlying tag string used for filtering/toggling is unchanged.

## Background (verify before editing)

- Owner quote: "metadata:language:en should read language: en, not metadata:language:en".
- Tag shape is confirmed as `metadata:<key>:<value>` (2 colons) from internal/database/tag_helpers.go:20-21 doc comment: 'metadata:source:<name> — last metadata apply provenance' and 'metadata:language:<code> — language of the applied metadata'.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'label={' web/src/components/library/TagCloud.tsx   # 1 hit, uses `${t.tag} (${t.count})` directly — chip label uses the raw tag string with no formatting
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In web/src/components/library/TagCloud.tsx, add a pure function `formatTagLabel(tag: string): string` near `isHiddenTagNamespace` (from part 1): if `tag.startsWith('metadata:')`, split on ':' into exactly 3 parts (`metadata`, key, value — value may itself contain no further colons per the observed shape), and return `${key}: ${value}`; otherwise return `tag` unchanged.
2. In `renderChip`, change `label={`${t.tag} (${t.count})`}` to `label={`${formatTagLabel(t.tag)} (${t.count})`}`.
3. Guard against a malformed metadata:* tag with fewer than 3 colon-segments (e.g. just `metadata:` or `metadata:foo`) by falling back to the raw tag string rather than throwing or rendering `undefined`.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_165.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A value containing a colon itself (unlikely per current writers, but not structurally forbidden) would be truncated by a naive 3-way split — use `tag.split(':').slice(2).join(':')` for the value segment to be safe rather than a strict 3-part split.

## Tests

- web/src/components/library/TagCloud.test.tsx — formatTagLabel('metadata:language:en') === 'language: en'.
- formatTagLabel('metadata:source:audible') === 'source: audible' (this case is also hidden per part 1, but the formatter must still be correct in isolation for future non-hidden metadata:* keys).
- formatTagLabel('science fiction & fantasy') === 'science fiction & fantasy' (non-metadata tags pass through unchanged).
- formatTagLabel('metadata:') === 'metadata:' (malformed input falls back safely, does not throw).

Anti-over-suppression test: `N/A — pure formatting, no suppression involved.` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `npm --prefix web test -- TagCloud` passes with the new formatting cases.
- [ ] Manual: a library with metadata:language:en tags shows chips reading 'language: en (N)'.
- [ ] Anti-over-suppression test: `N/A — pure formatting, no suppression involved.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_165.md`.

## Commit message

```
fix(web): Reformat metadata:* tags in Browse by Tag: strip prefix, 'ke (TODO L1350)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Ship in the same PR as part 1 (same file, same renderChip function).
