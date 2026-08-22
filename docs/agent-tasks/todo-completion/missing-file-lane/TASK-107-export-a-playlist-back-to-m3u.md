<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-107-export-a-playlist-back-to-m3u.md -->
<!-- version: 1.0.0 -->
<!-- guid: 676794b3-10cd-474f-b3f1-c23da8868b5c -->
<!-- last-edited: 2026-08-21 -->

# TASK-107 — Export a playlist back to .m3u (TODO.md L8646)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** small, single-endpoint feature with an existing playlist-membership accessor to build on · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8646 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Playlists — implement the whole surface** — owne" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-107-export-a-playlist-back-to-m3u" -b agent/missing-file-lane-107-export-a-playlist-back-to-m3u origin/main
cd "$REPO/.worktrees/missing-file-lane-107-export-a-playlist-back-to-m3u"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add GET /api/v1/playlists/:id/export.m3u that loads a UserPlaylist's resolved member books/files and writes a standard #EXTM3U file (one line per file's real FilePath, with #EXTINF duration+title comments) as the HTTP response body.

## Background (verify before editing)

- For a smart playlist, use MaterializedBookIDs (the last-evaluated set, per store.go's own comment on that field) rather than a live re-query, matching the read-mostly convention playlistDTO already uses in abs/playlists.go.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func.*Export\|ToM3U\|WriteM3U' internal/server/handlers/playlists.go internal/playlist/*.go internal/database/pebble_store_playlists.go   # 0 hits — no export-to-m3u handler exists
  ```

### Reuse — don't invent

- Use `GetUserPlaylist + MaterializedBookIDs for smart-playlist membership` in `internal/database/pebble_store_playlists.go` (verify: `grep -n 'func (p \*PebbleStore) GetUserPlaylist' internal/database/pebble_store_playlists.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add ExportPlaylistM3U(c *gin.Context) to internal/server/handlers/playlists.go, following this file's existing handler conventions.
2. Load the playlist via GetUserPlaylist(id), resolve member book_file paths (static: BookIDs; smart: MaterializedBookIDs).
3. Write `#EXTM3U\n#EXTINF:<duration>,<title>\n<path>\n` per entry, Content-Type audio/x-mpegurl.
4. Wire the route alongside ReorderPlaylist's route.
5. Bump file header.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_107.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A smart playlist never materialized (MaterializedBookIDs empty) exports an empty (header-only) file rather than erroring.

## Tests

- internal/server/handlers/playlists_export_test.go: TestExportPlaylistM3U_HappyPath — output has #EXTM3U header and one line per member in playlist order.
- TestExportPlaylistM3U_UnknownPlaylist404s

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/server/handlers/... -run ExportPlaylistM3U passes.
- [ ] GET the endpoint for a real 3-book static playlist returns valid #EXTM3U text with 3 path lines.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/... ./internal/server/handlers/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_107.md`.

## Commit message

```
feat(missing-file-lane): Export a playlist back to .m3u (TODO L8646)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `GET the endpoint for a real 3-book static playlist returns valid #EXTM3U text with 3 path lines.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Playlist entries carrying explicit timings (cue-derived) are a separate future chapter-offset source per this item's own cross-reference to chapters-backfill-from-duplicates (L8611) — out of scope for export itself.
