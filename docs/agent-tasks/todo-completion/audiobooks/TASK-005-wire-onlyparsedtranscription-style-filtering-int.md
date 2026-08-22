<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-005-wire-onlyparsedtranscription-style-filtering-int.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7a35bc36-4255-44ef-ac0d-51961cf18060 -->
<!-- last-edited: 2026-08-21 -->

# TASK-005 — Wire OnlyParsedTranscription-style filtering into the interactive audiobooks list endpoint (TODO.md L10728)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · audiobooks subagent · **Why:** touches 4 files across 2 packages (query parsing, ListFilters struct, and the actual filter predicate applied during both the indexed/pebble-scan path and the memdb path) and must respect the project's documented bareParamAllowList gotcha · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10728 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Transcription quality filter**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-005-wire-onlyparsedtranscription-style-filtering-int" -b agent/audiobooks-005-wire-onlyparsedtranscription-style-filtering-int origin/main
cd "$REPO/.worktrees/audiobooks-005-wire-onlyparsedtranscription-style-filtering-int"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new boolean field to `audiobooks.ListFilters` (e.g. `OnlyParsedTranscription bool`) mirroring `operations.FilterSpec.OnlyParsedTranscription`'s exact semantics (TranscribedTitle non-nil and non-empty-after-trim), parse it from a new `?only_parsed_transcription=true` query param in `ListAudiobooks` (internal/server/handlers/audiobooks/handler.go), and apply it as a predicate in the same place FingerprintStatus is applied in internal/audiobooks/service_filtering.go and service_query.go, so an interactive Library-page user (not just a background bulk op) can filter the list to books whose transcript actually parsed.

## Background (verify before editing)

- internal/operations/types.go:36-41 documents the exact semantics to replicate: 'restricts the resolved set to books whose Whisper intro parsed into a usable title (TranscribedTitle non-empty)... works for books transcribed before the statusUnparsed split too — no backfill required.'
- internal/server/metadata_ops.go:523-526 is the reference implementation of the predicate itself: `if f.OnlyParsedTranscription && (b.TranscribedTitle == nil || strings.TrimSpace(*b.TranscribedTitle) == "") { continue }`.
- CRITICAL gotcha (internal/server/handlers/audiobooks/handler.go:240-259, `bareParamAllowList`): the endpoint has a guard that rejects unrecognized bare query params against a 'KnownFilterFields' derivation; a name must be added to `bareParamAllowList` the moment it gets a real accessor on this endpoint, mirroring how `fingerprint_status` was pre-declared there before its accessor existed, or a request using the new param could be wrongly rejected — this project has a documented history of two near-misses on exactly this guard (see the comment block at L240-259).
- internal/audiobooks/service_types.go:132-137 (`strippedMemdbFields`) confirms `TranscribedTitle` is NOT stripped from memdb-resident Book copies — so, unlike `description`/`version_notes`/`book_sig_v1`, this predicate is safe to apply against the default memdb-backed list path without needing a Pebble re-fetch.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn OnlyParsedTranscription internal/operations/types.go internal/server/metadata_ops.go   # 3 hits, all in bulk-op resolution code — OnlyParsedTranscription exists only on operations.FilterSpec, used only for bulk-op targeting
  grep -n 'type ListFilters struct' -A20 internal/audiobooks/service_types.go   # no transcription-related field in the 20 lines — ListFilters (interactive list) has no such field
  grep -n only_parsed_transcription internal/server/handlers/audiobooks/handler.go   # 0 hits — the interactive handler parses no such query param
  grep -n FingerprintStatus internal/server/handlers/audiobooks/handler.go internal/audiobooks/service_types.go internal/audiobooks/service_filtering.go   # ≥5 hits across the 3 files — FingerprintStatus is the closest existing analog to copy the wiring pattern from
  ```

### Reuse — don't invent

- Use `FingerprintStatus wiring pattern (query parse → ListFilters field → service_filtering.go predicate)` in `internal/server/handlers/audiobooks/handler.go` (verify: `grep -n 'FingerprintStatus:  httputil.ParseQueryString' internal/server/handlers/audiobooks/handler.go`) — do NOT write a parallel helper.
- Use `existing boolean semantics to copy exactly (TranscribedTitle non-empty test)` in `internal/server/metadata_ops.go` (verify: `grep -n 'TranscribedTitle == nil' internal/server/metadata_ops.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add `OnlyParsedTranscription bool` to `ListFilters` in internal/audiobooks/service_types.go:78-96 (near FingerprintStatus).
2. In internal/server/handlers/audiobooks/handler.go, parse `only_parsed_transcription` via `httputil.ParseQueryBoolPtr` or the plain-bool helper used for `show_quarantined` at L595 (note: ListFilters.OnlyParsedTranscription is a plain bool, so parse as `c.Query("only_parsed_transcription") == "true"`, matching the show_quarantined pattern, not the *bool pattern used for IsPrimaryVersion) into the `filters := audiobookspkg.ListFilters{...}` literal at ~L513-523.
3. Add `"only_parsed_transcription": {}` to `bareParamAllowList` (L256-259) with a one-line comment explaining why.
4. In internal/audiobooks/service_filtering.go buildBookSummaryFilterWithLookupCount (~L994-1039): change line 994's `hasFPFilters := f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil` to also include `|| f.OnlyParsedTranscription`, THEN add the predicate: if `f.OnlyParsedTranscription && (b.TranscribedTitle == nil || strings.TrimSpace(*b.TranscribedTitle) == "")`, exclude the book -- copy the exact predicate from metadata_ops.go:524-527 verbatim.
5. In internal/audiobooks/service_query.go: change line 71's `hasFingerprintingFilters := f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil` to also include `|| f.OnlyParsedTranscription`, THEN add the same predicate in the fingerprinting-filters application block (~L400-415) as the sibling of the FingerprintStatus check.
6. Bump file-header versions on all 4 touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_005.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book whose TranscribedTitle is an empty string (not nil) — must also be excluded (per the exact reference predicate's TrimSpace check), not just nil-checked.
- Combined with other filters (e.g. `&library_state=organized`) — must AND together, not override; verify by testing with 2+ simultaneous query params.

## Tests

- internal/server/handlers/audiobooks/handler_test.go or a new test file — add a case: seed 2 books, one with TranscribedTitle set and one nil, GET /api/v1/audiobooks?only_parsed_transcription=true, assert only the parsed one is returned.
- internal/audiobooks/*_test.go — a unit test on the filtering predicate mirroring the existing pattern in internal/audiobooks/transcribed_title_pushdown_test.go (which already guards that TranscribedTitle survives the memdb summary pushdown — extend/pair with a test that OnlyParsedTranscription actually filters using it).
- Anti-over-suppression: a test asserting `only_parsed_transcription` OMITTED (default false) returns both books unfiltered — the new param must not accidentally become a default-on filter.

Anti-over-suppression test: `test: 'only_parsed_transcription omitted returns the full unfiltered set (including unparsed-transcript books)'` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/server/handlers/audiobooks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n OnlyParsedTranscription internal/audiobooks/service_types.go` returns 1 hit.
- [ ] `curl -s "$API/api/v1/audiobooks?only_parsed_transcription=true"` (or the equivalent test) returns only books with a non-empty TranscribedTitle.
- [ ] `make test` passes for internal/audiobooks and internal/server/handlers/audiobooks.
- [ ] Anti-over-suppression test: `test: 'only_parsed_transcription omitted returns the full unfiltered set (including unparsed-transcript books)'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/server/handlers/audiobooks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_005.md`.

## Commit message

```
feat(audiobooks): Wire OnlyParsedTranscription-style filtering into the intera (TODO L10728)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (``grep -n OnlyParsedTranscription internal/audiobooks/service_types.go` returns 1 hit.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This closes the literal contradiction between operations/types.go's doc comment (claims FilterSpec params all mirror the list endpoint) and reality for this one field — a small, well-scoped correctness fix, not new product surface.
