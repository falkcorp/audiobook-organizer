<!-- file: docs/agent-tasks/todo-completion/itunes/TASK-061-import-the-224-materialized-playlist-items-smart.md -->
<!-- version: 1.0.0 -->
<!-- guid: e1f9b4b7-da1e-4807-b3a4-c87b255ee554 -->
<!-- last-edited: 2026-08-21 -->

# TASK-061 — Import the 224 materialized-Playlist-Items smart playlists as static snapshots (no criteria parsing needed) (ITUNES-SMARTCRIT-PARSE)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · itunes subagent · **Why:** Real feature work extending an existing tested service, but the algorithm (resolve track IDs to books, store as static membership) is fully specified by the item text and reuses existing accessors — no novel design. · **Depends on:** TASK-184 · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 1517 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**ITUNES-SMARTCRIT-PARSE**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-061-import-the-224-materialized-playlist-items-smart" -b agent/itunes-061-import-the-224-materialized-playlist-items-smart origin/main
cd "$REPO/.worktrees/itunes-061-import-the-224-materialized-playlist-items-smart"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Extend the smart-playlist import path so that for playlists with materialized Playlist Items (no Smart Criteria parsing needed), it creates a UserPlaylist with type=smart (or a distinct 'static'/'snapshot' sub-type if the schema wants to distinguish 'imported from live membership' vs 'imported from parsed criteria' for future refresh semantics), populated by resolving each Playlist Item's Track ID → XML Track → Persistent ID → GetBookByITunesPersistentID. Gated behind part 1's coverage measurement passing a reasonable threshold (owner to set, e.g. >90%).

## Background (verify before editing)

- Track resolution is by persistent ID, not by file path — the item explicitly warns the XML Location values are Windows drive paths and must NOT be used for matching; use Persistent ID exclusively.
- internal/itunes/service/playlist_sync.go's existing MigrateSmartPlaylists already handles idempotency (skips playlists already imported by iTunes PID) and creates UserPlaylist rows — reuse that machinery rather than writing a parallel import path.
- The maintenance op (internal/plugins/maintenance/itunes_playlist_import.go) already has dry-run support, DryRun default true, and count reporting — this is the natural place to route the new path through, not a new endpoint.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func (p \*PlaylistSync) MigrateSmartPlaylists' internal/itunes/service/playlist_sync.go   # 1 hit at L112 — MigrateSmartPlaylists already exists and is the natural extension point
  grep -n 'itunesPlaylistImportDef' internal/plugins/maintenance/plugin.go   # 1 hit at L49 — the maintenance op that would call an extended MigrateSmartPlaylists is already registered
  ```

### Reuse — don't invent

- Use `MigrateSmartPlaylists (extend to accept a materialized-items path)` in `internal/itunes/service/playlist_sync.go` (verify: `grep -n 'func (p \*PlaylistSync) MigrateSmartPlaylists' internal/itunes/service/playlist_sync.go`) — do NOT write a parallel helper.
- Use `maintenance.itunes-playlist-import (already wired, dry-run gated, reports counts)` in `internal/plugins/maintenance/itunes_playlist_import.go` (verify: `grep -n 'ID:.*maintenance.itunes-playlist-import' internal/plugins/maintenance/itunes_playlist_import.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/itunes/service/playlist_sync.go, extend MigrateSmartPlaylists (or add a sibling method) to detect, per smart playlist, whether it carries materialized Playlist Items (check the parsed ITLPlaylist/XML playlist struct for a non-empty items/track-refs list — grep for the existing field name via `grep -n 'PlaylistItems\|TrackIDs' internal/itunes/*.go internal/itunes/service/*.go`).
2. For playlists WITH materialized items: for each item's Track ID, resolve the XML Tracks entry's Persistent ID, then call GetBookByITunesPersistentID (or file-level equivalent) to get the local book/book_file. Collect the resolved book IDs as the playlist's static membership.
3. Create the UserPlaylist row via whatever existing helper MigrateSmartPlaylists already uses for criteria-based playlists, but store resolved membership directly instead of (or in addition to, per schema) a translated DSL query — check the UserPlaylist schema for a static-membership field vs. a query field (`grep -n 'type UserPlaylist' -A 30 internal/database/*.go` or itunes store).
4. Report exact imported/skipped/unresolved-track counts, and per part 1's requirement, verify by re-reading the DB after the run rather than trusting return values.
5. Wire this as an addition to the existing maintenance.itunes-playlist-import op rather than a new op ID, since it shares the dry-run/params/CapLibraryRead contract already built there.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_itunes_061.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A Playlist Item whose Persistent ID does not resolve (per part 1's coverage gap) should be skipped and counted, not silently dropped without a number — report unresolved-item counts per playlist.
- Idempotency: re-running the import must not duplicate playlists already imported by iTunes PID (reuse the existing skip-by-PID logic).

## Tests

- internal/itunes/service/playlist_sync_test.go — a new test using a REAL (or realistically-shaped) blob/fixture with materialized Playlist Items, not the current hand-built `ITLPlaylist{IsSmart: true}` shortcut that the item calls out as never having exercised real data — the fix must specifically avoid repeating that mistake.
- Assert on actual imported membership (a non-empty, CORRECT book-ID list matching the fixture's expected resolution), not just a non-nil/non-empty Rules slice — per the item's own warning that a non-empty result is not evidence given the parser's tolerant-by-design error handling.

Anti-over-suppression test: `The new test must assert CORRECT resolved membership content, not merely a non-empty result — this is the item's own stated bar, since the sibling ParseSmartCriteria bug (part 3) proved that a non-empty/no-error result is not evidence of correctness.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/itunes/service/... ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] Dry-run against the owner's real XML reports counts matching or exceeding the 224/116,822 figures (allowing for library changes since 2026-08-10).
- [ ] A non-dry-run import creates UserPlaylist rows; re-reading the DB confirms membership matches the resolved book IDs, not just that rows exist.
- [ ] Anti-over-suppression test: `The new test must assert CORRECT resolved membership content, not merely a non-empty result — this is the item's own stated bar, since the sibling ParseSmartCriteria bug (part 3) proved that a non-empty/no-error result is not evidence of correctness.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/itunes/service/... ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_itunes_061.md`.

## Commit message

```
feat(itunes): Import the 224 materialized-Playlist-Items smart playlists a (ITUNES-SMARTCRIT-PARSE)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`Dry-run against the owner's real XML reports counts matching or exceeding the 224/116,822 figures (allowing for library changes since 2026-08-10).`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: this creates real UserPlaylist rows from a resolved book mapping — a resolution bug would silently populate playlists with wrong books. Depends on part 1 (PID coverage measurement) passing before this ships to the owner's real library.
