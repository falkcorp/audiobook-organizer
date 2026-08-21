<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-106-import-found-playlist-files-m3u-m3u8-pls-cue-xsp.md -->
<!-- version: 1.0.0 -->
<!-- guid: 263e3e9a-07e9-492d-87ae-fb5ee4df7ec5 -->
<!-- last-edited: 2026-08-21 -->

# TASK-106 — Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) during scan, resolving entries to book_file rows (TODO.md L8646)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** four file formats to parse, entry-to-book_file resolution with a real 38.2%-missing-book_file-row caveat this item itself flags, and scan-pipeline hook wiring · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 8646 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Playlists — implement the whole surface** — owne" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-106-import-found-playlist-files-m3u-m3u8-pls-cue-xsp" -b agent/missing-file-lane-106-import-found-playlist-files-m3u-m3u8-pls-cue-xsp origin/main
cd "$REPO/.worktrees/missing-file-lane-106-import-found-playlist-files-m3u-m3u8-pls-cue-xsp"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

During scan, detect .m3u/.m3u8/.pls/.cue/.xspf playlist files, parse their entries, resolve each entry path to a book_file row (never store the raw path, so a later reorganize doesn't silently break the playlist), and create a static UserPlaylist via the existing CreateUserPlaylist path.

## Background (verify before editing)

- 38.2% of books were in the no-book_file-row state as of 2026-08-05 per this item's own warning — entries pointing at such files will silently drop; sequence this AFTER relink/import work (relink-unlinked-books already exists) or the import will look lossy for unrelated reasons.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func parseM3UFile' internal/scanner/scanner.go   # 1 hit at L1742 — used by the case ".m3u", ".m3u8": branch (~L1824) purely for file-grouping, not playlist import — parseM3UFile already exists but only GROUPS files within a single book (the .m3u/.m3u8/.cue scanner case branches), not to create UserPlaylist rows
  grep -n '\.pls\|\.xspf' internal/scanner/scanner.go   # 0 hits — ParsePLS/ParseXSPF support does not exist yet — no .pls/.xspf parsing exists at all
  ```

### Reuse — don't invent

- Use `CreateUserPlaylist (static-type creation)` in `internal/database/pebble_store_playlists.go` (verify: `grep -n 'func (p \*PebbleStore) CreateUserPlaylist' internal/database/pebble_store_playlists.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add internal/playlist/import.go with one parser per format: ParseM3U(path) ([]string, error) (handling #EXTM3U/#EXTINF comment lines), ParsePLS, ParseCue (track offsets + one referenced audio file), ParseXSPF (XML track list).
2. Add ResolveEntriesToBookFiles(store, paths []string) — looks up each path via the store's existing file-path lookup and returns matched book_file IDs plus a list of unresolved paths (logged, never silently dropped from the return value).
3. Add ImportPlaylistFile(store, path string) (*database.UserPlaylist, error) that dispatches by extension, resolves entries, and calls CreateUserPlaylist with Type: static and BookIDs from the resolved set.
4. Hook into the scan pipeline (find where scanner.go detects non-audio files of interest, or add a post-scan pass) to call ImportPlaylistFile for each detected playlist file.
5. Add file headers to all new files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_106.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Empty playlist file (0 entries) — decide and document whether that's a no-op or an empty static playlist.
- A .cue file with no explicit track titles falls back to generated 'Track N' names.

## Tests

- internal/playlist/import_test.go: TestParseM3U_ExtractsPaths, TestParsePLS_ExtractsPaths, TestParseCue_ExtractsOffsetsAndFile, TestParseXSPF_ExtractsPaths — one fixture per format.
- TestResolveEntriesToBookFiles_UnresolvedEntriesReportedNotDropped — anti-over-suppression: an entry with no matching book_file row appears in the unresolved list, not just vanishes from the count.

Anti-over-suppression test: `TestResolveEntriesToBookFiles_UnresolvedEntriesReportedNotDropped` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/playlist/... ./internal/scanner/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/playlist/... passes.
- [ ] Importing a fixture .m3u with 3 known-good paths and 1 unknown path creates a static playlist with 3 members and logs/reports the 1 unresolved entry.
- [ ] Anti-over-suppression test: `TestResolveEntriesToBookFiles_UnresolvedEntriesReportedNotDropped` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/playlist/... ./internal/scanner/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_106.md`.

## Commit message

```
feat(missing-file-lane): Import found playlist files (.m3u/.m3u8/.pls/.cue/.xspf) dur (TODO L8646)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/playlist/... passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Sequence after relink-unlinked-books-derived work per the item's own 38.2% warning.
