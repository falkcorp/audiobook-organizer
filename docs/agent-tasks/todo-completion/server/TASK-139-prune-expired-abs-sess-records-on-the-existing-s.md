<!-- file: docs/agent-tasks/todo-completion/server/TASK-139-prune-expired-abs-sess-records-on-the-existing-s.md -->
<!-- version: 1.1.0 -->
<!-- guid: 793c9bad-b007-4071-8da4-8fe10eefda0c -->
<!-- last-edited: 2026-09-02 -->

# TASK-139 — Prune expired abs_sess: records on the existing session-cleanup schedule (ABS-SYNC)

> **Status 2026-09-02:** ✅ DONE — PR #2737 merged 2026-08-23 (56d4de4fb).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server subagent · **Why:** Mechanical: add one interface method + one call inside an already-existing loop, following an adjacent line's exact pattern · **Depends on:** none · **Wave:** 3

Source: `TODO.md` line 10298 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**ABS-SYNC: prune expired `abs_sess:` records on a" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-13.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-139-prune-expired-abs-sess-records-on-the-existing-s" -b agent/server-139-prune-expired-abs-sess-records-on-the-existing-s origin/main
cd "$REPO/.worktrees/server-139-prune-expired-abs-sess-records-on-the-existing-s"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add DeleteExpiredABSSessions(now time.Time) (int, error) to the serverCredentialStore interface in internal/server/server_ops_store.go, and call it from the same ticker loop in server_lifecycle.go that already calls DeleteExpiredSessions, so revoked/expired abs_sess: records are pruned on the same 10-minute cadence instead of accumulating forever.

## Background (verify before editing)

- PebbleStore.DeleteExpiredABSSessions(now time.Time) (int, error) at internal/database/pebble_store_abssession.go:308 removes revoked/past-expiry ABS sessions plus both of their index entries — it mirrors DeleteExpiredSessions per its own doc comment (L307).
- server_lifecycle.go:608-629 already runs a background goroutine on a 10-minute ticker calling s.Ops().DeleteExpiredSessions(time.Now()) and logging deleted>0 at Info, warn on error.
- server_ops_store.go's ServerOpsStore interface (L36-45) embeds serverAuthStore -> serverCredentialStore (L223-227), which is the narrow interface s.Ops() satisfies for auth/session methods; DeleteExpiredABSSessions is not yet a member.
- Neither internal/database/mock_store.go nor internal/database/mocks/mock_store.go has a DeleteExpiredABSSessions stub yet, confirmed by grep returning 0 hits in both.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'DeleteExpiredABSSessions' internal/database/pebble_store_abssession.go   # hits only in the func def (~L308) and its doc comment (~L306-307) — DeleteExpiredABSSessions is implemented on PebbleStore but has no production caller
  grep -n 's.Ops().DeleteExpiredSessions(time.Now())' internal/server/server_lifecycle.go   # 1 hit ~L619 — the existing session-cleanup sweep calls DeleteExpiredSessions on a 10-minute ticker
  grep -n 'DeleteExpiredSessions(now time.Time)' internal/server/server_ops_store.go   # 1 hit ~L226, inside serverCredentialStore — serverCredentialStore is the narrow interface s.Ops() exposes for this sweep and does not yet include the ABS method
  ```

### Reuse — don't invent

- Use `the 10-minute sessionCleanupTicker loop shape` in `internal/server/server_lifecycle.go` (verify: `grep -n 'sessionCleanupTicker := time.NewTicker' internal/server/server_lifecycle.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/server_ops_store.go, add `DeleteExpiredABSSessions(now time.Time) (int, error)` as a new line inside the `serverCredentialStore` interface (~L224-227), alphabetically placed the way its sibling methods are (after CreateSession, before ListAllAPIKeys, matching the existing ordering convention in that interface).
2. In internal/server/server_lifecycle.go, inside the existing `for { select { case <-sessionCleanupTicker.C: ... } }` block (~L616-627), add a second call right after the existing `s.Ops().DeleteExpiredSessions(time.Now())` block: `if deletedABS, err := s.Ops().DeleteExpiredABSSessions(time.Now()); err != nil { sessionLog.Warn("failed to clean up expired ABS sessions: %v", err) } else if deletedABS > 0 { sessionLog.Info("cleaned up %d expired/revoked ABS sessions", deletedABS) }` — same log style as the existing call, sharing the same sessionLog and ticker (do not add a second ticker).
3. Add a `DeleteExpiredABSSessionsFunc func(now time.Time) (int, error)` field + method to internal/database/mock_store.go's MockStore, following the exact pattern of the adjacent DeleteExpiredSessionsFunc (~L252, L1454-1456).
4. Regenerate or hand-add the mockery-style mock in internal/database/mocks/mock_store.go for DeleteExpiredABSSessions, mirroring the DeleteExpiredSessions block there (~L8444-8499) — check whether this repo runs `go generate` for mocks (look for a //go:generate mockery directive near the top of iface_auth.go or similar) and prefer running that over hand-editing if so.
5. Bump version headers on every touched Go file per file-headers.md.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_139.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- ABS may not always be enabled/wired (s.Ops() can be nil per the existing `if s.Ops() != nil` guard at L609) — the new call must live inside that same existing guard, not add a second one.
- If DeleteExpiredABSSessions errors independently of DeleteExpiredSessions, both must be logged and neither should abort the other's cleanup for that tick.

## Tests

- internal/server/server_lifecycle_test.go (or a new focused test file): assert that the session-cleanup goroutine calls both DeleteExpiredSessions and DeleteExpiredABSSessions on tick — use a mock/fake Ops() store that records calls, advance a fake/injected ticker (or reduce the ticker interval via a test hook if one already exists for the sibling test), and assert both methods were invoked.
- internal/database/pebble_store_abssession_test.go: confirm the existing DeleteExpiredABSSessions unit tests still pass unchanged (no signature change to the underlying PebbleStore method, only wiring it into a new caller).

Anti-over-suppression test: `N/A — this is a straightforward wiring addition, not a filter/guard` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] D
- [ ] e
- [ ] l
- [ ] e
- [ ] t
- [ ] e
- [ ] E
- [ ] x
- [ ] p
- [ ] i
- [ ] r
- [ ] e
- [ ] d
- [ ] A
- [ ] B
- [ ] S
- [ ] S
- [ ] e
- [ ] s
- [ ] s
- [ ] i
- [ ] o
- [ ] n
- [ ] s
- [ ] '
- [ ]  
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] s
- [ ] e
- [ ] r
- [ ] v
- [ ] e
- [ ] r
- [ ] /
- [ ] s
- [ ] e
- [ ] r
- [ ] v
- [ ] e
- [ ] r
- [ ] _
- [ ] l
- [ ] i
- [ ] f
- [ ] e
- [ ] c
- [ ] y
- [ ] c
- [ ] l
- [ ] e
- [ ] .
- [ ] g
- [ ] o
- [ ]  
- [ ] s
- [ ] h
- [ ] o
- [ ] w
- [ ] s
- [ ]  
- [ ] a
- [ ]  
- [ ] c
- [ ] a
- [ ] l
- [ ] l
- [ ]  
- [ ] i
- [ ] n
- [ ] s
- [ ] i
- [ ] d
- [ ] e
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] s
- [ ] e
- [ ] s
- [ ] s
- [ ] i
- [ ] o
- [ ] n
- [ ] -
- [ ] c
- [ ] l
- [ ] e
- [ ] a
- [ ] n
- [ ] u
- [ ] p
- [ ]  
- [ ] t
- [ ] i
- [ ] c
- [ ] k
- [ ] e
- [ ] r
- [ ]  
- [ ] b
- [ ] l
- [ ] o
- [ ] c
- [ ] k
- [ ]  
- [ ] (
- [ ] 0
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] H
- [ ] E
- [ ] A
- [ ] D
- [ ] )
- [ ] ;
- [ ]  
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] D
- [ ] e
- [ ] l
- [ ] e
- [ ] t
- [ ] e
- [ ] E
- [ ] x
- [ ] p
- [ ] i
- [ ] r
- [ ] e
- [ ] d
- [ ] A
- [ ] B
- [ ] S
- [ ] S
- [ ] e
- [ ] s
- [ ] s
- [ ] i
- [ ] o
- [ ] n
- [ ] s
- [ ] '
- [ ]  
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] s
- [ ] e
- [ ] r
- [ ] v
- [ ] e
- [ ] r
- [ ] /
- [ ] s
- [ ] e
- [ ] r
- [ ] v
- [ ] e
- [ ] r
- [ ] _
- [ ] o
- [ ] p
- [ ] s
- [ ] _
- [ ] s
- [ ] t
- [ ] o
- [ ] r
- [ ] e
- [ ] .
- [ ] g
- [ ] o
- [ ]  
- [ ] s
- [ ] h
- [ ] o
- [ ] w
- [ ] s
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] n
- [ ] e
- [ ] w
- [ ]  
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] f
- [ ] a
- [ ] c
- [ ] e
- [ ]  
- [ ] m
- [ ] e
- [ ] t
- [ ] h
- [ ] o
- [ ] d
- [ ]  
- [ ] n
- [ ] e
- [ ] x
- [ ] t
- [ ]  
- [ ] t
- [ ] o
- [ ]  
- [ ] D
- [ ] e
- [ ] l
- [ ] e
- [ ] t
- [ ] e
- [ ] E
- [ ] x
- [ ] p
- [ ] i
- [ ] r
- [ ] e
- [ ] d
- [ ] S
- [ ] e
- [ ] s
- [ ] s
- [ ] i
- [ ] o
- [ ] n
- [ ] s
- [ ]  
- [ ] (
- [ ] c
- [ ] u
- [ ] r
- [ ] r
- [ ] e
- [ ] n
- [ ] t
- [ ] l
- [ ] y
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] o
- [ ] n
- [ ] l
- [ ] y
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ]  
- [ ] i
- [ ] s
- [ ]  
- [ ] L
- [ ] 2
- [ ] 2
- [ ] 7
- [ ]  
- [ ] D
- [ ] e
- [ ] l
- [ ] e
- [ ] t
- [ ] e
- [ ] E
- [ ] x
- [ ] p
- [ ] i
- [ ] r
- [ ] e
- [ ] d
- [ ] S
- [ ] e
- [ ] s
- [ ] s
- [ ] i
- [ ] o
- [ ] n
- [ ] s
- [ ] )
- [ ] ;
- [ ]  
- [ ] g
- [ ] o
- [ ]  
- [ ] b
- [ ] u
- [ ] i
- [ ] l
- [ ] d
- [ ]  
- [ ] .
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] s
- [ ] u
- [ ] c
- [ ] c
- [ ] e
- [ ] e
- [ ] d
- [ ] s
- [ ] ,
- [ ]  
- [ ] p
- [ ] r
- [ ] o
- [ ] v
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] P
- [ ] e
- [ ] b
- [ ] b
- [ ] l
- [ ] e
- [ ] S
- [ ] t
- [ ] o
- [ ] r
- [ ] e
- [ ]  
- [ ] a
- [ ] l
- [ ] r
- [ ] e
- [ ] a
- [ ] d
- [ ] y
- [ ]  
- [ ] s
- [ ] a
- [ ] t
- [ ] i
- [ ] s
- [ ] f
- [ ] i
- [ ] e
- [ ] s
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] w
- [ ] i
- [ ] d
- [ ] e
- [ ] n
- [ ] e
- [ ] d
- [ ]  
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] f
- [ ] a
- [ ] c
- [ ] e
- [ ]  
- [ ] (
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] d
- [ ] a
- [ ] t
- [ ] a
- [ ] b
- [ ] a
- [ ] s
- [ ] e
- [ ] /
- [ ] p
- [ ] e
- [ ] b
- [ ] b
- [ ] l
- [ ] e
- [ ] _
- [ ] s
- [ ] t
- [ ] o
- [ ] r
- [ ] e
- [ ] _
- [ ] a
- [ ] b
- [ ] s
- [ ] s
- [ ] e
- [ ] s
- [ ] s
- [ ] i
- [ ] o
- [ ] n
- [ ] .
- [ ] g
- [ ] o
- [ ] :
- [ ] 3
- [ ] 0
- [ ] 8
- [ ] )
- [ ] ;
- [ ]  
- [ ] g
- [ ] o
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] .
- [ ] /
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] d
- [ ] a
- [ ] t
- [ ] a
- [ ] b
- [ ] a
- [ ] s
- [ ] e
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] .
- [ ] /
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] d
- [ ] a
- [ ] t
- [ ] a
- [ ] b
- [ ] a
- [ ] s
- [ ] e
- [ ] /
- [ ] m
- [ ] o
- [ ] c
- [ ] k
- [ ] s
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] .
- [ ] /
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] s
- [ ] e
- [ ] r
- [ ] v
- [ ] e
- [ ] r
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] -
- [ ] c
- [ ] o
- [ ] u
- [ ] n
- [ ] t
- [ ] =
- [ ] 1
- [ ]  
- [ ] e
- [ ] x
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] 0
- [ ] .
- [ ] Anti-over-suppression test: `N/A — this is a straightforward wiring addition, not a filter/guard` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_139.md`.

## Commit message

```
feat(server): Prune expired abs_sess: records on the existing session-clea (ABS-SYNC)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -rn 'DeleteExpiredABSSessions' internal/database/pebble_store_abssession.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Low risk, additive-only change; no data migration needed since DeleteExpiredABSSessions already exists and is tested standalone.
