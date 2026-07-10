<!-- file: docs/agent-tasks/ux-small-items/TASK-02-hash-chain-detail-view.md -->
<!-- version: 1.0.0 -->
<!-- guid: 326e76a4-b85c-475a-8f7b-59752e9fff45 -->
<!-- last-edited: 2026-07-10 -->

# TASK-02 — Show the file hash chain in the book-file detail view (HASH-CHAIN-2 / #1270)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.
**File-ownership:** none for `internal/server/` (this task changes NO Go files); shares `TODO.md` with TASK-01 — do not start until TASK-01's PR is merged (wave 2).

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · frontend-integration subagent · **Why:** MUI layout judgment + graceful-missing-data handling, beyond a mechanical edit · **Depends on:** TASK-01 (TODO.md same-file serialization only)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ux-small-items-hash-chain-detail-view" -b agent/ux-small-items-hash-chain-detail-view origin/main
cd "$REPO/.worktrees/ux-small-items-hash-chain-detail-view"
git rebase origin/main
```

## Goal

Render each book file's hash chain — **Download → Original → Post-metadata → Current** — in the expanded per-file rows of the book detail Files tab, so users can see when/where a file changed (GitHub issue #1270, TODO item HASH-CHAIN-2). The backend already stores and serializes all four hashes; this is a frontend-only task plus one type addition. REUSE the existing `Tooltip` import and table structure in `BookDetailVersionGroup.tsx` — do not add a new API call, a new endpoint, or a new fetch hook: the data already rides the existing book-files response.

## Background (verify before editing)

- Backend fields all exist with JSON tags in `internal/database/store.go`: `DownloadHash` (`download_hash`, as-downloaded), `OriginalFileHash` (`original_file_hash`, after iTunes/external tagger), `PostMetadataHash` (`post_metadata_hash`, after an AO metadata write), `FileHash` (`file_hash`, current). Backend is VERIFY-ONLY for you — you change no Go files.
- Frontend `interface BookFile` in `web/src/types/index.ts` already has `file_hash` / `original_file_hash` / `post_metadata_hash` but is MISSING `download_hash`.
- ⚠ **DATA-SOURCE RULE (the mistake this brief exists to prevent):** the chain fields live on `BookFile`, NOT on `Book`. The frontend `Book` interface has `file_hash`/`original_file_hash`/`organized_file_hash` but NO `download_hash` and NO `post_metadata_hash`. In `BookDetailVersionGroup.tsx`, `version` is a `Book` (`versions: Book[]` prop) — reading `version.download_hash` yields `undefined` forever and the em-dash fallback would hide the bug. Source the chain from the component's **`bookFiles: BookFile[]` prop** (~:54), which the Files tab passes down from `BookDetailFilesTab.tsx` (~:156) and which VersionGroup already maps into per-file rows on its `isCurrent` branch (~:207). Anchor note: the anchors JSON names `BookDetailFilesTab.tsx` as the detail view — FilesTab delegates per-file rendering to VersionGroup, which is why VersionGroup is the edit site. `bookFiles` only populates for the current version; non-current versions' segment rows have no hash data and are out of scope.
- No hash is rendered anywhere under `web/src` today (verified 2026-07-10: `grep -rn 'OriginalFileHash\|PostMetadataHash\|DownloadHash' web/src` → zero matches — those are Go names; the snake_case type fields exist but are unused in UI).
- The per-file expanded row lives in `web/src/components/bookdetail/BookDetailVersionGroup.tsx`, which already renders `version.file_path` in a `TableCell` (use that as the visual neighborhood only — NOT the data source, see the rule above) and already imports MUI `Tooltip`.
- Hash values may be empty/absent on many rows (fields are `omitempty` / optional). Treat missing as UNKNOWN — render an em dash `—`, never hide the chain and never treat missing as an error.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "DownloadHash\|PostMetadataHash\|OriginalFileHash" internal/database/store.go   # backend fields, >=3 hits (verify-only)
  grep -n "interface BookFile" web/src/types/index.ts                                     # type to extend, 1 hit
  grep -n "download_hash" web/src/types/index.ts                                          # expect 0 hits BEFORE your edit
  grep -n "version.file_path" web/src/components/bookdetail/BookDetailVersionGroup.tsx    # insertion neighborhood, >=1 hit
  grep -n "bookFiles: BookFile\[\]" web/src/components/bookdetail/BookDetailVersionGroup.tsx  # DATA SOURCE prop, 1 hit (~:54)
  grep -n "bookFiles={bookFiles}" web/src/components/bookdetail/BookDetailFilesTab.tsx        # confirms FilesTab passes bookFiles down to VersionGroup, 1 hit (~:156)
  grep -n "bookFiles.map" web/src/components/bookdetail/BookDetailVersionGroup.tsx        # per-file mapping to hook into, >=1 hit (~:208)
  grep -n "Tooltip" web/src/components/bookdetail/BookDetailVersionGroup.tsx              # existing import to REUSE, >=1 hit
  grep -n "HASH-CHAIN-2" TODO.md                                                          # TODO line to check off
  ```
  Zero-hit on any expected-hit grep at execution time means STOP and report.

## Step-by-step

1. Run the re-verify greps above (never trust line numbers from this brief).
2. In `web/src/types/index.ts`, add `download_hash?: string;` inside `interface BookFile`, adjacent to the existing `file_hash?: string;` line. Purely additive — do not reorder or rename existing fields.
3. In `BookDetailVersionGroup.tsx`, inside the expanded per-version content, add a compact hash-chain row PER FILE, **sourced from each `BookFile` in the `bookFiles` prop** (hook into the existing `isCurrent && bookFiles.length > 0` per-file mapping at ~:207 — carry the hash fields through or render alongside each file row): four labeled entries in chain order `Download → Original → Post-metadata → Current`, from `f.download_hash`, `f.original_file_hash`, `f.post_metadata_hash`, `f.file_hash` where `f` is a `BookFile`. ⛔ NEVER read `version.download_hash`/`version.post_metadata_hash` — `version` is a `Book` and lacks those fields (undefined forever, masked by the dashes). Truncate each shown hash to its first 12 characters; wrap each in the existing `Tooltip` showing the full value. If a value is null/undefined/empty string, render `—` (em dash) for that link — the chain row itself always renders when the row is expanded.
4. Keep the change purely additive — do not modify existing table columns, do not change any component props/signatures, do not touch fetch logic.
5. Add/extend a frontend test (mirror the existing test style next to the component if one exists — check with `ls web/src/components/bookdetail/*.test.*`; otherwise extend the nearest suite under `web/src` that renders the files tab) covering: (a) POSITIVE case — a `BookFile` fixture with all four hashes present → four truncated hash VALUES actually render (assert on the truncated hash text, not just the label; this is the case that catches wiring the chain to the wrong type); (b) NO hashes present → the row still renders with four `—` dashes and nothing crashes (this is the anti-over-suppression case: the happy files-tab path must survive).
6. In `TODO.md`, tick HASH-CHAIN-2 to `[x]` and append ` — shipped, see #1270 / this PR` (only that line; TASK-01 already fixed the ratings section — leave everything else alone).
7. Bump file headers (version + last-edited) on every touched file; keep existing guids.

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. Also run `npm run build` inside `web/` (or `make build`) to prove the type addition compiles, and `make test-all` for the frontend suite.

## Acceptance criteria

- [ ] `grep -n "download_hash" web/src/types/index.ts` hits (type extended — inside `interface BookFile`).
- [ ] `grep -n "download_hash" web/src/components/bookdetail/BookDetailVersionGroup.tsx` hits (chain rendered) AND `grep -n "version\.download_hash\|version\.post_metadata_hash" web/src/components/bookdetail/BookDetailVersionGroup.tsx` → 0 hits (chain sourced from `bookFiles`/`BookFile`, not `Book`).
- [ ] POSITIVE render test green: a file WITH a `download_hash` renders its truncated value (string-in-source is NOT sufficient evidence).
- [ ] Anti-over-suppression: test proving a file with NO hash values still renders its expanded row (dashes, no crash) is green.
- [ ] Tests green (Minimal CI + frontend suite); vet/lint clean on changed files.
- [ ] File headers bumped on every changed file.
- [ ] PR body contains `Closes #1270`.

## Commit message

```
feat(ui): show file hash chain in book-file detail view (HASH-CHAIN-2, #1270)

Backend has stored download/original/post-metadata/current hashes since
HASH-CHAIN-1 (#1722) but no UI surfaced them. Renders the chain per expanded
file row with tooltips; missing hashes render as dashes, never errors.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ux-small-items-hash-chain-detail-view
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "download_hash" web/src/types/index.ts` hits AND `grep -n "download_hash" web/src/components/bookdetail/BookDetailVersionGroup.tsx` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; display-only change, no data, no schema, backend untouched.
