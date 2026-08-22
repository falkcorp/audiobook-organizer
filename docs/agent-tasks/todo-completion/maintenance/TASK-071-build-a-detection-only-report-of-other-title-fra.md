<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-071-build-a-detection-only-report-of-other-title-fra.md -->
<!-- version: 1.0.0 -->
<!-- guid: f592a62b-538e-4afa-9107-94580cd9f7f0 -->
<!-- last-edited: 2026-08-21 -->

# TASK-071 — Build a detection-only report of other title-fragment author rows (the 57 rows beginning with '-') (TODO.md L3602)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** requires designing a report-only heuristic (rows beginning with '-' plus a broader dirty-shape scan) and a new maintenance op, but no mutation logic — no prod-data risk · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 3602 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Check how many other author rows are title fragmen" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-05.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-071-build-a-detection-only-report-of-other-title-fra" -b agent/maintenance-071-build-a-detection-only-report-of-other-title-fra origin/main
cd "$REPO/.worktrees/maintenance-071-build-a-detection-only-report-of-other-title-fra"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new REPORT-ONLY maintenance op (e.g. maintenance.author-title-fragment-scan) that walks every author row, flags any whose name (a) begins with a leading hyphen '-' (the specific giveaway named in this item), or (b) fails the exported looksLikePersonName shape check outright (not just as part of a split decision), and logs a count + sample of flagged author IDs/names/book-counts to the activity log — matching owner decision #11's 'detection-only counter, fix deferred' pattern. No renames, merges, or deletes.

## Background (verify before editing)

- internal/dedup/author.go:210 looksLikePersonName is currently unexported and used only inside SplitCompositeAuthorName's comma branch — it will need to be exported (e.g. renamed to LooksLikePersonName, with call sites updated) or duplicated at a lower fidelity for this report op to reuse it from internal/plugins/maintenance
- internal/plugins/maintenance/author.go:84-117 author-split-scan is the closest existing sibling: a LivenessManual, report-shaped maintenance op over all authors

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'TitleFragmentAuthor\|title-fragment-author\|author.title.fragment.report' internal/plugins/maintenance/*.go   # 0 hits — no existing op reports on title-fragment author names (a loose 'title.fragment|titleFragment' regex false-positives on unrelated prose in author_conjunction_repair.go/_test.go describing a different feature's exclusion rule)
  grep -n 'ID:.*author-split-scan' internal/plugins/maintenance/author.go   # 1 hit ~L89 — the existing author-split-scan op is the closest sibling / registration template
  grep -n 'func looksLikePersonName' internal/dedup/author.go   # 1 hit, L210 (unexported — see step 1 on exporting or duplicating minimally) — looksLikePersonName is the shape-check to reuse for detecting existing rows that would now be rejected
  ```

### Reuse — don't invent

- Use `looksLikePersonName shape heuristic (unexported — must be exported or its logic duplicated for use from the maintenance package)` in `internal/dedup/author.go` (verify: `grep -n 'func looksLikePersonName' internal/dedup/author.go`) — do NOT write a parallel helper.
- Use `author-split-scan op registration pattern (report-style, LivenessManual, no apply path)` in `internal/plugins/maintenance/author.go` (verify: `grep -n 'func (p \*Plugin) authorSplitScanDef' internal/plugins/maintenance/author.go`) — do NOT write a parallel helper.

## Step-by-step

1. Export looksLikePersonName as dedup.LooksLikePersonName(part string) bool in internal/dedup/author.go (rename in place; update the one internal call site in SplitCompositeAuthorName to use the exported name too, or keep an unexported wrapper — coordinator's call, note either is fine).
2. Create internal/plugins/maintenance/author_title_fragment_report.go with an OperationDef for maintenance.author-title-fragment-scan: LivenessManual, no schedule (manual-trigger only, since this is exploratory), Capabilities: []sdk.Capability{sdk.CapLibraryRead}.
3. In the Run function, list all authors (find the existing all-authors accessor used by author-split-scan or GetAllAuthorBookCounts's author enumeration path), and for each: flag if strings.HasPrefix(name, "-") OR !dedup.LooksLikePersonName(name) (calling the shape check directly on the full name, not as a split-decision component).
4. Report via reporter.Log/activity: total authors scanned, count flagged, and up to 100 sample rows (id, name, book count) — reuse the existing per-author book-count accessor (GetAllAuthorBookCounts, filtered view is fine for a report) so each flagged row shows how many books would be affected.
5. Register the op in internal/plugins/maintenance/plugin.go's op list alongside author-split-scan.
6. Bump version headers.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_071.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- author name is empty string — must not flag or panic
- author name is exactly '-' with nothing else — flag it, but don't crash on HasPrefix of a 1-char string
- an author with 0 books that still LOOKS like a title fragment — still worth reporting even though it's cheap to delete outright, since the report's job is enumeration, not judgment

## Tests

- internal/dedup/author_test.go: TestLooksLikePersonName_ExportedNameUnchangedBehavior — the renamed/exported function still passes the existing looksLikePersonName test cases (regression guard on the rename)
- internal/plugins/maintenance/author_title_fragment_report_test.go: TestAuthorTitleFragmentScan_FlagsHyphenPrefixed — an author named '-Something' is flagged
- internal/plugins/maintenance/author_title_fragment_report_test.go: TestAuthorTitleFragmentScan_DoesNotFlagRealNames — an author named 'Ludwig van Beethoven' (particle-containing real name) is NOT flagged (anti-over-suppression: proves the report doesn't just flag everything with a lowercase word)

Anti-over-suppression test: `TestAuthorTitleFragmentScan_DoesNotFlagRealNames` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run AuthorTitleFragmentScan passes
- [ ] running the op against a seeded corpus containing 3 known-bad names and 3 known-good names reports exactly 3 flagged
- [ ] Anti-over-suppression test: `TestAuthorTitleFragmentScan_DoesNotFlagRealNames` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_071.md`.

## Commit message

```
feat(maintenance): Build a detection-only report of other title-fragment author (TODO L3602)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -rn 'TitleFragmentAuthor\|title-fragment-author\|author.title.fragment.report' internal/plugins/maintenance/*.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Per owner decision #9/#11 pattern: this is a REPORT op only. Do not add a delete/merge/apply path in this task — that is future work gated on a design decision the owner has not made (see L3586/3588/3589).
