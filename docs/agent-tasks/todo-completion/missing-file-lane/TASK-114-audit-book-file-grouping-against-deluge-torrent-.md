<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-114-audit-book-file-grouping-against-deluge-torrent-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 446e8f48-e3c0-405a-b00a-e762fd38d8ac -->
<!-- last-edited: 2026-08-21 -->

# TASK-114 — Audit book/file grouping against Deluge torrent file-list membership (read-only, tier 1 of the item's own 3-tier ambition) (TODO.md L8738)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** cross-references torrent file-membership against book grouping at library scale, feeding a regroup classifier signal — same class of judgment work as recommender-tuning items elsewhere in this codebase · **Depends on:** TASK-113 · **Wave:** 2

Source: `TODO.md` line 8738 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Use Deluge's per-torrent file list as ground tru" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-114-audit-book-file-grouping-against-deluge-torrent-" -b agent/missing-file-lane-114-audit-book-file-grouping-against-deluge-torrent- origin/main
cd "$REPO/.worktrees/missing-file-lane-114-audit-book-file-grouping-against-deluge-torrent-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build the AUDIT tier only (per the item's own 3-tier ambition: audit → evidence → repair): for each torrent Deluge knows about, fetch its file list, resolve those files to book_file rows, and flag any case where the torrent's files are split across multiple books OR multiple torrents were merged into one book, as a candidate grouping error — read-only, never auto-repairs.

## Background (verify before editing)

- Coverage is inherently partial (only books still seeded and known to Deluge) — absent coverage must mean 'no opinion', never 'wrong', per the item.
- A torrent may span SEVERAL books (a series pack) — torrent membership is an upper bound on ONE book, not proof of one book; pair any 'these files should be one book' signal with the existing duration guard used elsewhere in regroup logic rather than trusting torrent membership alone.
- Files may have moved/renamed since acquisition — match by size+content, never path alone, per the item.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rn 'audit\|grouping' internal/deluge/*.go internal/plugins/deluge/*.go   # 0 hits — no existing code audits grouping against torrent file lists
  grep -n 'fields' internal/deluge/client.go | head -5   # ≥1 hit — the client already supports requesting a fields subset (extendable to the file list) from core.get_torrent_status
  ```

### Reuse — don't invent

- Use `Client.GetTorrentStatus with a fields param for the torrent's file list` in `internal/deluge/client.go` (verify: `grep -n 'fields' internal/deluge/client.go | head -5`) — do NOT write a parallel helper.

## Step-by-step

1. Reuse (do not duplicate) the read-only Deluge RPC client from internal/deluge/client.go and, if L8707 is built first, its shared credential/session plumbing.
2. Add internal/deluge/grouping_audit.go: for each known torrent, call GetTorrentStatus with the file-list field, resolve each file to a book_file row by matching on size+content hash (not path alone).
3. Group resolved book_file rows by their current BookID; a torrent whose files resolve to >1 distinct BookID is a 'split' candidate; track which torrents map into the SAME BookID for the 'merged' direction too.
4. Apply the duration-guard-equivalent check (find it in internal/linkintegrity or internal/plugins/maintenance) before flagging a 'should be one book' candidate from a single torrent spanning >1 book, to avoid the series-pack-is-not-one-book trap.
5. Write a candidates-only report — NO auto-repair; Capabilities should be CapLibraryRead only.
6. Register as a new read-only maintenance-style op.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_114.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A torrent whose files match by content hash but at a DIFFERENT path than the book's current FilePath must still resolve correctly (files may have moved since acquisition).

## Tests

- TestGroupingAudit_FlagsSplitTorrent — fixture torrent whose files resolve to 2 different BookIDs is flagged.
- TestGroupingAudit_UncoveredBookIsNoOpinionNotWrong — anti-over-suppression: a book with zero matching torrents does not appear in the audit's flagged output at all.
- TestGroupingAudit_SeriesPackDoesNotFlagAsMerge — a torrent legitimately containing multiple distinct books (matched via the duration guard) is NOT flagged as a grouping error.

Anti-over-suppression test: `TestGroupingAudit_UncoveredBookIsNoOpinionNotWrong` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/deluge/... -run GroupingAudit passes all three cases.
- [ ] Anti-over-suppression test: `TestGroupingAudit_UncoveredBookIsNoOpinionNotWrong` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_114.md`.

## Commit message

```
feat(missing-file-lane): Audit book/file grouping against Deluge torrent file-list me (TODO L8738)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/deluge/... -run GroupingAudit passes all three cases.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The item's own roadmap (evidence-feeding the regroup classifier, then repair) is future work beyond this audit tier — do not build repair here; it must stay review-gated like every other regroup proposal, per the item's own text.
