<!-- file: docs/agent-tasks/todo-completion/itunes/TASK-064-add-a-part-disc-chapter-track-filename-parser-so.md -->
<!-- version: 1.0.0 -->
<!-- guid: 856ad3b1-f713-493c-96eb-c5122c33aade -->
<!-- last-edited: 2026-08-21 -->

# TASK-064 — Add a Part->disc / Chapter->track filename parser so 'P0-C0'-style folders stop falling to ambiguous (REGROUP-PARTCHAPTER-PARSER)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · itunes subagent · **Why:** Adds a new pattern into a dense, carefully evidence-ranked classification decision tree (classifyGroup, ~L832-1075) where ordering and guard interactions with existing rules (anthology markers, generic-title guard, edition markers) matter — a naive regex add risks reclassifying folders that should stay ambiguous for other reasons · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10366 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**REGROUP-PARTCHAPTER-PARSER**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-13.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/itunes-064-add-a-part-disc-chapter-track-filename-parser-so" -b agent/itunes-064-add-a-part-disc-chapter-track-filename-parser-so origin/main
cd "$REPO/.worktrees/itunes-064-add-a-part-disc-chapter-track-filename-parser-so"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a filename-level regex recognizing the 'P<n>-C<n>' (Part/Chapter) naming convention (e.g. '01 P0-C0.mp3', '07 P1-C6.mp3' — non-contiguous chapter numbers across parts) and feed it into classifyGroup (~fs_regroup_shape.go:832) as a new confident-collapse signal, parallel to the existing discDirRe/chapterSubdirRe folder-shape signals, so such folders classify as KindMultidisc (or a new dedicated kind if Part/Chapter shouldn't be conflated with true multi-disc — this call is left to implementation judgment guided by assignDiscTrack's existing 'no real discs' invariant) instead of falling through to KindAmbiguous.

## Background (verify before editing)

- The Mistborn-style case has files like '01 P0-C0.mp3', '07 P1-C6.mp3' inside one folder — a Part/Chapter naming scheme with NON-CONTIGUOUS chapter numbers across parts (so a naive 'are these numbers contiguous' check would reject it).
- assignDiscTrack (fs_regroup_shape.go:1142) already treats ALL collapsed books as DiscNumber=0 with sequential TrackNumber by owner decision (2026-07-26) — so recognizing this naming pattern only needs to change WHICH kind classifyGroup assigns, not how numbering is persisted afterward.
- The existing disc/track fix mentioned in the item (multi-disc folder detection) is unaffected by this gap — Part/Chapter is a DIFFERENT naming convention from 'Disc N' folders, requiring its own regex.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'regexp.MustCompile' internal/itunes/service/fs_regroup_shape.go   # ~12 hits, none matching a Part/Chapter token pattern — no P#/C# filename pattern exists among the current naming regexes
  grep -n 'KindAmbiguous' internal/itunes/service/fs_regroup_shape.go   # ≥5 hits — KindAmbiguous is the fallback classification such folders receive today
  grep -n 'DiscNumber is ALWAYS 0' internal/itunes/service/fs_regroup_shape.go   # 1 hit ~L1131 — disc numbers are never persisted from folder structure — always 0, sequential track order — so this is a classification-only fix, not a renumbering fix
  ```

### Reuse — don't invent

- Use `chapterSubdirRe / discDirRe as the pattern-matching style to follow` in `internal/itunes/service/fs_regroup_shape.go` (verify: `grep -n 'var chapterSubdirRe = regexp.MustCompile' internal/itunes/service/fs_regroup_shape.go`) — do NOT write a parallel helper.
- Use `assignDiscTrack — already correct, do not modify; new parser only affects classification, not this function` in `internal/itunes/service/fs_regroup_shape.go` (verify: `grep -n 'func assignDiscTrack' internal/itunes/service/fs_regroup_shape.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/itunes/service/fs_regroup_shape.go's classifyGroup function (~L832-1075) end to end to understand the existing decision order (anthology check, edition-marker check, disc-dir check, generic-title guard, etc.) before inserting a new branch.
2. Add a new regex, e.g. `var partChapterRe = regexp.MustCompile(`(?i)\bP(\d+)[-_]C(\d+)\b`)`, near the other filename-token regexes (~L461-519), with a doc comment explaining the non-contiguous-numbering caveat (chapter numbers restart or skip across parts; the Part index, not the Chapter index, is the grouping key).
3. In classifyGroup, add a detection step: if a majority of a folder's member filenames match partChapterRe, treat the group as a confident collapse (mirror the existing discDirRe-driven branch's structure) rather than falling through to the KindAmbiguous default. Sort members by (Part, Chapter) numeric value (not lexical) before calling sortMembers/assignDiscTrack, since 'P1-C6' before 'P10-C1' would sort wrong lexically.
4. Ensure assignDiscTrack is called AFTER the new sort so DiscNumber stays 0 and TrackNumber is sequential per the existing owner decision — do not change assignDiscTrack itself.
5. Bump the file's version header.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_itunes_064.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Part and Chapter numbers can both start at 0 (P0-C0) — the parser must not treat 0 as 'missing/invalid' the way some of the other numeric regexes in this file treat a bare leading zero.
- Chapter numbers are NON-CONTIGUOUS across parts by design per the item — do not add a 'chapters must be 1..N contiguous' validation gate, since that would reject the exact case this task exists to fix.

## Tests

- internal/itunes/service/fs_regroup_shape_test.go: TestClassifyGroup_PartChapterNaming_CollapsesAsMultidisc — construct a synthetic folder with files '01 P0-C0.mp3'..'07 P1-C6.mp3' (non-contiguous chapter numbers, exactly the Mistborn case from the item) and assert classifyGroup returns a confident (non-ambiguous) kind with TrackNumber running 1..N in Part-then-Chapter order.
- TestClassifyGroup_PartChapterNaming_MixedWithOtherFiles_StaysAmbiguous — a folder where only SOME files match the P#-C# pattern and others don't should NOT be force-collapsed (anti-over-suppression: prove the new regex doesn't over-eagerly claim folders it shouldn't).

Anti-over-suppression test: `TestClassifyGroup_PartChapterNaming_MixedWithOtherFiles_StaysAmbiguous` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/itunes/service/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/itunes/service/... -run TestClassifyGroup_PartChapter` passes
- [ ] a manual dry-run of maintenance.regroup-shattered-ai against a fixture containing the Mistborn-style folder now reports it as a confident collapse instead of ambiguous
- [ ] Anti-over-suppression test: `TestClassifyGroup_PartChapterNaming_MixedWithOtherFiles_StaysAmbiguous` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/itunes/service/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_itunes_064.md`.

## Commit message

```
feat(itunes): Add a Part->disc / Chapter->track filename parser so 'P0-C0' (REGROUP-PARTCHAPTER-PARSER)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'regexp.MustCompile' internal/itunes/service/fs_regroup_shape.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The item frames this as a 'fast-follow, consider' item, not a hard requirement — still fully actionable and briefable, just lower urgency than the security/data-loss items in this scope.
