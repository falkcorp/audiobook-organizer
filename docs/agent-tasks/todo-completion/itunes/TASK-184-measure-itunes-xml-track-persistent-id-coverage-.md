<!-- file: docs/agent-tasks/todo-completion/itunes/TASK-184-measure-itunes-xml-track-persistent-id-coverage-.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5b5b4540-c6c0-4409-915d-d21ddc1e3a90 -->
<!-- last-edited: 2026-09-02 -->

# TASK-184 — Measure iTunes XML track Persistent ID coverage against the local DB before promising a Playlist-Items snapshot import (ITUNES-SMARTCRIT-PARSE)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — internal/itunes/pid_coverage.go and _test.go absent; cmd/pid-census pre-dates the brief; 0 commits to any exact_file since 2026-08-21. Recommendation: keep - it is the measurement gate before promising a Playlist-Items snapshot import.

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · itunes subagent · **Why:** A bounded, mechanical measurement against existing, already-proven parsing infrastructure (ParseITL + GetBookByITunesPersistentID) -- small and low-risk, mostly wiring rather than new parsing logic. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1517 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**ITUNES-SMARTCRIT-PARSE**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-184-measure-itunes-xml-track-persistent-id-coverage-" -b agent/itunes-184-measure-itunes-xml-track-persistent-id-coverage- origin/main
cd "$REPO/.worktrees/itunes-184-measure-itunes-xml-track-persistent-id-coverage-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add internal/itunes/pid_coverage.go with a ComputePIDCoverage(store, itlPath) function (modeled on pid_integrity.go's ComputePIDIntegrity) that parses the owner's iTunes library via the existing ParseITL/plist_parser.go plumbing, and for each track's Persistent ID checks whether GetBookByITunesPersistentID resolves it to a book in the local DB. Report total XML track PIDs, count/percent resolved, and (if feasible from the parsed structure) the resolution rate restricted to tracks referenced by the 224 Playlist-Items-bearing smart playlists. Wire a --coverage flag into cmd/pid-census/main.go to invoke it.

## Background (verify before editing)

- internal/itunes/pid_integrity.go's ComputePIDIntegrity already proves the ParseITL -> lib.Tracks[i].PersistentID -> pidToHex plumbing works end-to-end against real data; this item's coverage measurement is a straightforward sibling computation (resolution rate against GetBookByITunesPersistentID) rather than new parsing work.
- The 224/292 vs 66/292 vs 2/292 split (materialized Playlist Items vs criteria-only vs neither) is already established by the item text; this measurement only needs to run against the 224 (+their track refs) subset to be decision-useful, though reporting the whole-library rate is also useful context.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func (p \*PebbleStore) GetBookByITunesPersistentID' internal/database/pebble_store.go   # 1 hit ~L1179 — a persistent-ID lookup accessor exists to check coverage against
  grep -n 'plist:"Persistent ID"\|plist:"Tracks"' internal/itunes/plist_parser.go   # 2 hits, ~L28 and ~L35 — internal/itunes already parses track-level Persistent IDs from an XML plist library
  grep -n 'lib.Tracks\[i\].PersistentID\|func ComputePIDIntegrity' internal/itunes/pid_integrity.go   # ≥2 hits — an existing census (pid_integrity.go) already parses lib.Tracks[i].PersistentID via ParseITL, giving a direct plumbing precedent, but computes duplicate-PID shape, not coverage/resolution rate
  grep -n 'func main' cmd/pid-census/main.go   # 1 hit — cmd/pid-census already exists as the CLI entry point this measurement should extend
  ```

### Reuse — don't invent

- Use `GetBookByITunesPersistentID (existing PID lookup)` in `internal/database/pebble_store.go` (verify: `grep -n 'func (p \*PebbleStore) GetBookByITunesPersistentID' internal/database/pebble_store.go`) — do NOT write a parallel helper.
- Use `ParseITL + plist_parser.go's Tracks/PersistentID parsing, already proven working by pid_integrity.go` in `internal/itunes/pid_integrity.go` (verify: `grep -n 'func ComputePIDIntegrity' internal/itunes/pid_integrity.go`) — do NOT write a parallel helper.
- Use `cmd/pid-census's existing flag-driven CLI structure to extend with a --coverage flag` in `cmd/pid-census/main.go` (verify: `grep -n 'flag.Bool' cmd/pid-census/main.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read pid_integrity.go's ComputePIDIntegrity (internal/itunes/pid_integrity.go) fully as the structural template -- it already does ParseITL + per-track PID extraction + GetAllBookFilesCore-based lookups.
2. Write internal/itunes/pid_coverage.go: parse the library via ParseITL(itlPath) (or the XML-specific path if the source is Library.xml rather than the binary .itl -- confirm which format the owner's source file actually is before choosing the parser), iterate lib.Tracks, and for each track's PersistentID call store.GetBookByITunesPersistentID(pidToHex(...)) to check resolution.
3. Locate the smart-playlist / Playlist-Items parsing path (internal/itunes/service/playlist_sync.go:112, MigrateSmartPlaylists) to identify which playlists are the 224 'materialized items' ones and which track IDs they reference, if the playlist-track linkage is exposed by that function's return type.
4. Emit a report: {total_xml_tracks, total_referenced_by_224_playlists, resolved_count, resolved_percent, top N unresolved examples with their titles for spot-check}.
5. Add a --coverage flag to cmd/pid-census/main.go (following the existing flag pattern for --full, --repair etc.) that invokes ComputePIDCoverage and prints the report.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_itunes_184.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A Persistent ID may resolve to a book_file rather than directly to a book -- GetBookByITunesPersistentID resolves at the BOOK level (confirmed: database.Book has its own ITunesPersistentID field distinct from BookFile's), so confirm which granularity the source data actually needs before assuming book-level resolution is sufficient for the 224-playlist use case.

## Tests

- internal/itunes/pid_coverage_test.go: a small synthetic ITL/plist fixture containing a mix of resolvable and unresolvable Persistent IDs, asserting the report's counts are exact.

Anti-over-suppression test: `N/A -- measurement task.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./cmd/pid-census/... ./internal/itunes/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go run ./cmd/pid-census --db <copy> --itl <copy> --coverage` runs to completion and prints a resolution percentage with example unresolved PIDs for manual sanity check.
- [ ] Anti-over-suppression test: `N/A -- measurement task.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./cmd/pid-census/... ./internal/itunes/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_itunes_184.md`.

## Commit message

```
feat(itunes): Measure iTunes XML track Persistent ID coverage against the  (ITUNES-SMARTCRIT-PARSE)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'func (p \*PebbleStore) GetBookByITunesPersistentID' internal/database/pebble_store.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This gates the actual snapshot-import feature (not in this scope) -- do not build that until this measurement shows acceptable coverage, per the item's own explicit instruction. pid_integrity.go is a close sibling doing a related but distinct census (duplicate PIDs, not coverage) -- do not confuse a green pid_integrity run with this item being done.
