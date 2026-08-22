<!-- file: docs/agent-tasks/todo-completion/itunes/TASK-185-report-the-itunes-listened-in-progress-status-pi.md -->
<!-- version: 1.0.0 -->
<!-- guid: d0161dc1-3383-4582-b942-884801664589 -->
<!-- last-edited: 2026-08-21 -->

# TASK-185 — Report the iTunes listened/in-progress status pipeline's actual wiring gap: PositionSync is fully built but its maintenance op is an unimplemented stub (PLAYBACK-IMPORT)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · itunes subagent · **Why:** The hard part of the investigation (tracing 3 packages, finding the exact gap) is already done by this rescope with grep-verified citations; the remaining work is writing it up plus confirming questions 4 (audio-file-embedded progress) and 5 (API/UI surfacing), each independently answerable by grep/read. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 2181 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**PLAYBACK-IMPORT**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-185-report-the-itunes-listened-in-progress-status-pi" -b agent/itunes-185-report-the-itunes-listened-in-progress-status-pi origin/main
cd "$REPO/.worktrees/itunes-185-report-the-itunes-listened-in-progress-status-pi"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Write docs/audits/2026-08-21-itunes-playback-import-wiring.md answering all 4 of the item's original questions with grep-verified evidence, centered on the now-confirmed finding: the import path DOES parse and write PlayCount/Bookmark onto book rows, a full PositionSync service exists and is instantiated (internal/itunes/service/service.go:121), but the maintenance op wrapping it (internal/plugins/itunes/position_sync.go, op ID `itunes.position-sync`) is a literal unimplemented stub that always errors, and its cron schedule was deliberately removed for that reason. Also answer question 4 (does the scanner read audio-file-embedded progress -- confirmed NO generic read found, only scanner.go:2772-2773's existing<->scanned ITunesBookmark merge-preservation logic, not a fresh read of file-embedded position) and question 5 (does the API/UI surface it -- confirmed the reading.go handler exposes GetBookState/ListByStatus, a real API surface exists; frontend rendering was not fully traced in this pass and should be confirmed by whoever picks up the report). Do NOT implement any repair, backfill, or the stub itself in this task -- wiring `p.svc.Positions.Sync()` into runPositionSync is a real, concrete future fix, but it is gated on silent-failure Wave 5 per the item's own explicit caution about position_sync.go's read-failure-vs-no-prior-state ambiguity.

## Background (verify before editing)

- internal/plugins/itunes/position_sync.go:23-25's own comment already self-documents the exact history: the op used to run on a '*/10 * * * *' cron but always no-op'd (returning errNotImplemented), burning a green op-history row every 10 minutes, until the schedule was removed 2026-07-17 -- this IS the 'unwired pipeline' the TODO item hypothesized, now confirmed with a precise file:line rather than a suspicion.
- internal/readstatus/readstatus.go:144's discard-on-error behavior and internal/database/pebble_store_playback.go's stale status-index-leak (both cited by the original item as related-but-distinct context, not in scope to fix here) were not independently re-verified in this pass -- carry them forward as unconfirmed context, not re-verified findings.
- Silent-failure Wave 5 (project memory: 'Wave 0 undone, 926 issues, gate RED') is the gate for actually implementing the stub, per the item's own explicit warning that a failed read in position_sync.go's pull path is currently indistinguishable from 'no prior state' and could silently overwrite real playback position if the op starts running for real.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func RecomputeUserBookState' internal/readstatus/readstatus.go   # 1 hit ~L62 — internal/readstatus.RecomputeUserBookState exists at the cited function
  grep -n 'plist:"Play Count"\|plist:"Bookmark"' internal/itunes/plist_parser.go   # 2 hits, ~L49 and ~L53 — iTunes PlayCount/PlayDate/Bookmark fields ARE parsed from the source library file
  grep -n 'ITunesPlayCount:\|ITunesBookmark:' internal/itunes/import.go internal/itunes/service/importer.go   # multiple hits confirming import.go:396/398 and importer.go:871/883/1893/1895 all set these fields — these parsed values ARE written onto the book struct at import time
  grep -n 'func (p \*PositionSync) Sync\|func (p \*PositionSync) pullBookmarks' internal/itunes/service/position_sync.go   # 2 hits — a fully-implemented bidirectional position sync service exists, distinct from the import path
  grep -n 'TODO: Implement iTunes position sync operation\|Schedule removed 2026-07-17' internal/plugins/itunes/position_sync.go   # 2 hits, ~L23 and ~L38 — THE GAP: the maintenance op that would run this sync is an unimplemented stub, and its schedule was removed for exactly that reason
  ```

### Reuse — don't invent

- Use `internal/itunes/service/position_sync.go's fully-implemented PositionSync.Sync -- the code the stub needs to call` in `internal/itunes/service/position_sync.go` (verify: `grep -n 'func (p \*PositionSync) Sync' internal/itunes/service/position_sync.go`) — do NOT write a parallel helper.

## Step-by-step

1. Confirm question 4 more thoroughly than this rescope's pass did: search internal/scanner/ for any read of .m4b bookmark atoms or embedded chapter position beyond the existing ITunesBookmark-preservation-during-rescan logic at scanner.go:2772-2773 (which preserves an EXISTING bookmark value across a re-scan, not a fresh read from the audio file itself).
2. Confirm question 5's frontend half: grep web/src for consumption of the reading.go API surface (GetBookState/ListByStatus/SetPosition) to determine whether a stored UserBookState value is actually rendered anywhere in the UI, or only reachable via direct API calls.
3. Write up docs/audits/2026-08-21-itunes-playback-import-wiring.md with the findings from this rescope (questions 1-3, fully answered with citations above) plus steps 1-2's confirmation of questions 4-5, explicitly stating which of 'never parsed', 'parsed but never written', 'written but never surfaced', or 'wired but the maintenance op that runs it is a stub' applies to each stage.
4. Explicitly flag the concrete, ungated future fix this report enables: wiring `p.svc.Positions.Sync()` into internal/plugins/itunes/position_sync.go:37-41's runPositionSync, and restoring its cron schedule (comment at L23-25 says exactly what to do once implemented) -- but note this is gated on silent-failure Wave 5 landing first, per the item's own caution, and is NOT to be implemented in this task.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_itunes_185.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The finding that PositionSync is fully built but never invoked is itself worth flagging prominently -- it is a stronger, more actionable finding than a generic 'investigate whether this is unwired', and should not be buried as just one bullet among four in the writeup.

## Tests

- (none)

Anti-over-suppression test: `N/A -- investigation task.` — a known-good input still passes with the new guard active.

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] docs/audits/2026-08-21-itunes-playback-import-wiring.md exists and answers all 4 original questions with specific file:line citations, distinguishing 'not implemented' from 'implemented but unwired' from 'wired but buggy' for each of the 4 stages.
- [ ] Anti-over-suppression test: `N/A -- investigation task.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_itunes_185.md`.

## Commit message

```
feat(itunes): Report the iTunes listened/in-progress status pipeline's act (PLAYBACK-IMPORT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `docs/audits/2026-08-21-itunes-playback-import-wiring.md exists and answers all 4 original questions with specific file:line citations, distinguishing 'not implemented' from 'implemented but unwired' from 'wired but buggy' for each of the 4 stages.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This rescope found the definitive answer to the item's core question (yes, unwired -- and precisely where) rather than leaving it fully open; whoever picks this up should spend most of their time on the report-writing and the two remaining unconfirmed questions (4 and 5), not re-deriving what's already cited here. The actual fix (wiring the stub) is a natural follow-on TODO once Wave 5 lands, but is explicitly out of scope for this item.
