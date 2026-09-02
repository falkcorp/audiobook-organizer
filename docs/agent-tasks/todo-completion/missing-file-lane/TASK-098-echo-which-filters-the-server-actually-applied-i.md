<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-098-echo-which-filters-the-server-actually-applied-i.md -->
<!-- version: 1.1.0 -->
<!-- guid: a377dabf-43d4-4686-b0c1-e41f497d72ca -->
<!-- last-edited: 2026-09-02 -->

# TASK-098 — Echo which filters the server actually applied in the /audiobooks list response (TODO.md L7736)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — grep applied_filters handlers/audiobooks/handler.go -> 0 hits; applied_filters_test.go ABSENT; filters query parse still handler.go:501. Recommendation: keep.

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** small, well-scoped handler + response-shape change with an already-validated filter list to draw from · **Depends on:** none · **Wave:** 3

Source: `TODO.md` line 7736 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Move library filtering/search into the Go server" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-098-echo-which-filters-the-server-actually-applied-i" -b agent/missing-file-lane-098-echo-which-filters-the-server-actually-applied-i origin/main
cd "$REPO/.worktrees/missing-file-lane-098-echo-which-filters-the-server-actually-applied-i"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add an applied_filters array to ListAudiobooks' JSON envelope, built from the already-validated filters.FieldFilters/filters.PerUserFilters plus the simple params (library_state, tag, tags, fingerprint_status, coverage_percent_min/max, sort_by/sort_order) that buildListResponse actually used, so the frontend can render active-filter chips from ground truth.

## Background (verify before editing)

- handler.go already has a fully declared, validated filter path: unknown fields get a 400 naming the field and listing valid alternatives (see internal/server/handlers/audiobooks/unknown_filter_field_test.go, 2026-08-14), and empty values are rejected too (empty_field_filter_test.go) — the 'fail-open silently returns the whole library' bug this TODO item measured is ALREADY FIXED for the filters= JSON param.
- The response envelope built via httputil.RespondWithOK(c, resp) at the end of ListAudiobooks (~L604) does not currently include what was applied — only items/count/limit/offset.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'applied_filters' internal/server/handlers/audiobooks/handler.go   # 0 hits — no applied_filters key exists in the ListAudiobooks response today
  grep -n 'filtersJSON := c.Query("filters")' internal/server/handlers/audiobooks/handler.go   # 1 hit ~L530 — the filters param already parses into a typed []FieldFilter via a validated path
  ```

### Reuse — don't invent

- Use `audiobookspkg.FieldFilter / filters.FieldFilters / filters.PerUserFilters` in `internal/server/handlers/audiobooks/handler.go` (verify: `grep -n 'FieldFilters' internal/server/handlers/audiobooks/handler.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/server/handlers/audiobooks/handler.go, after `filters` is fully populated (after the `if filtersJSON := c.Query("filters")` block, ~L574), build an `appliedFilters []map[string]string` slice from filters.FieldFilters and filters.PerUserFilters (field+value pairs), plus the simple params already parsed (library_state, tag, tags, fingerprint_status, coverage_percent_min/max, sort_by/sort_order) when non-empty.
2. Add `applied_filters` to the response gin.H built in buildListResponse (or wrap its result) so RespondWithOK's payload includes it alongside items/count/limit/offset.
3. Add internal/server/handlers/audiobooks/applied_filters_test.go asserting: (a) no filters sent → applied_filters is an empty array, not omitted; (b) filters=[{"field":"title","value":"Dune"}] → applied_filters contains {"field":"title","value":"Dune"}; (c) library_state=imported → applied_filters includes {"field":"library_state","value":"imported"}.
4. Bump the file-header version on handler.go per this repo's file-header convention.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_098.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A request with NO filters must still include applied_filters as an empty array, never omit the key.
- PerUserFilters (per-user read_status/progress fields) must be included too, since those are also something the server actually applied.

## Tests

- internal/server/handlers/audiobooks/applied_filters_test.go: TestListAudiobooks_AppliedFilters_EchoesFieldFilters — asserts a filters= param round-trips into the response's applied_filters array with the same field/value.
- TestListAudiobooks_AppliedFilters_EmptyWhenNoFiltersSent — anti-over-suppression: asserts an unfiltered request still returns applied_filters as [] (not omitted or null).

Anti-over-suppression test: `TestListAudiobooks_AppliedFilters_EmptyWhenNoFiltersSent` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/audiobooks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] curl with filters=[{"field":"library_state","value":"imported"}] returns a body containing "applied_filters":[{"field":"library_state","value":"imported"}]
- [ ] go test ./internal/server/handlers/audiobooks/... -run AppliedFilters passes
- [ ] Anti-over-suppression test: `TestListAudiobooks_AppliedFilters_EmptyWhenNoFiltersSent` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/audiobooks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_098.md`.

## Commit message

```
feat(missing-file-lane): Echo which filters the server actually applied in the /audio (TODO L7736)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `curl with filters=[{"field":"library_state","value":"imported"}] returns a body containing "applied_filters":[{"field":"library_state","value":"imported"}]` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is the smallest independently-shippable slice of the much larger L7736 item; the filter-AST/GraphQL design question is filed as a separate needs_design object, same todo_line part 2.
