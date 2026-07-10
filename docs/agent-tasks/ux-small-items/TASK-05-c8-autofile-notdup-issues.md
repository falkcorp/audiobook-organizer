<!-- file: docs/agent-tasks/ux-small-items/TASK-05-c8-autofile-notdup-issues.md -->
<!-- version: 1.0.0 -->
<!-- guid: 65b46317-900c-40a0-951d-25acb580b6f4 -->
<!-- last-edited: 2026-07-10 -->

# TASK-05 — C8: auto-file a GitHub issue per not_dup cluster, dry-run + human-gated (C8 / #1447) ⚠ review-critical

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.
**File-ownership:** none for `internal/server/` (new files under `internal/plugins/dedup/` only); shares `TODO.md` with TASK-07 — wave 6, last.

**Priority:** P3 · **Effort:** M · **Recommended subagent:** Sonnet-class · go-backend subagent · **Why:** gating logic (confirm-hash refusal) is safety-critical and outward-acting; not a mechanical edit · **Depends on:** ⛔ EXTERNAL BLOCKERS — (a) INIT-1's mining-rule fix + gold-label re-mine merged AND applied on prod, (b) a HUMAN has confirmed dispatch of this task, (c) TASK-07 merged (TODO.md serialization). **Do not start on your own judgment.**

## ⛔ BLOCKED — check before anything else

The `not_dup` labels this op clusters over were proven contaminated on 2026-07-08 (rule-mined
mislabels: "part-vs-whole" misfiring on the ms/sec duration bug + "no resolvable files").
Filing GitHub issues from contaminated clusters files garbage. INIT-1 owns the fix + re-mine.
If you cannot point at (a) the merged INIT-1 mining-rule PR and (b) an explicit human go-ahead
for this task, STOP NOW and report `BLOCKED: 1 — TASK-05 awaiting INIT-1 re-mine + human confirm`.

## ⛔ START HERE (do this first, exactly — ONLY after the blockers above clear)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ux-small-items-c8-autofile-notdup-issues" -b agent/ux-small-items-c8-autofile-notdup-issues origin/main
cd "$REPO/.worktrees/ux-small-items-c8-autofile-notdup-issues"
git rebase origin/main
```

## Goal

Implement op `dedup.autofile-notdup-issues` (TODO.md C8, GitHub issue #1447): after a labeled-dataset backfill, aggregate `not_dup` label clusters where the rule-suppressed count exceeds a threshold, and surface each cluster as a GitHub issue (systematic false-positive sources for human review). **Issue creation is an OUTWARD action:** the op defaults to dry-run (report only); apply mode structurally refuses to run unless given a `confirm` param equal to the dry-run report's hash, and an actual apply run is only ever dispatched after a real AskUserQuestion decision by the coordinator — a text-reply approval does not count. Concrete parameters are pinned INLINE in this brief (Step 2: `threshold` default 5; Step 3: cluster key = the `LabeledExample` field tuple `(LabelReason, SignatureRelation, FolderRelation)`) — this brief is self-contained and authoritative. The earlier sketch in `docs/agent-tasks/dedup-dataset/TASK-05-autobug-not-dup-clusters.md` is provenance only (it left both values open); you do NOT need to open it, and this brief supersedes its dispatch gating.

## Background (verify before editing)

- TODO.md C8 line (~:810) is unchecked; zero implementation exists (verified 2026-07-10: `grep -rln 'CreateIssue\|issues\.Create\|gh issue create' internal/` → 0 files).
- Sibling op to MIRROR for registration + structure: `dedup.cleanup-orphan-author-embeddings` in `internal/plugins/dedup/cleanup_orphan_author_embeddings.go` (+ its `_test.go` asserting `def.ID`). Copy its op-definition shape; do NOT invent a new registration mechanism.
- Labels live in the dedup label store (`internal/database/dedup_label.go`); label fields include `LabelSource` (`human` vs `rule`/`auto_high_conf`).
- Concurrency house rule: any per-cluster/per-book loop over library-scale data uses the existing bounded-pool pattern (`registry.RunItems` — the sibling ops in this package use it), NEVER a bare `for range`.
- Missing/empty cluster fields are UNKNOWN, not disqualifying: a cluster with an unparseable sample-book title still counts toward its threshold — degrade the issue body, don't drop the cluster.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "\*\*C8\*\*" TODO.md                                                                 # TODO line, 1 hit
  grep -rn "dedup.cleanup-orphan-author-embeddings" internal/plugins/dedup/cleanup_orphan_author_embeddings.go   # sibling to mirror, >=1 hit
  grep -rn "RunItems" internal/plugins/dedup/ | head -5                                        # bounded-pool pattern, >=1 hit
  grep -n "not_dup" internal/database/dedup_label.go                                           # label constants/fields, >=1 hit
  grep -n "LabelReason\|SignatureRelation\|FolderRelation" internal/database/dedup_label.go     # cluster-key fields (Step 3 tuple), >=3 hits
  grep -rln "CreateIssue\|gh issue create" internal/                                            # expect 0 hits BEFORE your edit
  ```
  Zero-hit on any expected-hit grep means STOP and report.

## Step-by-step

1. Re-verify anchors; re-confirm the ⛔ blockers cleared.
2. NEW `internal/plugins/dedup/autofile_notdup_issues.go`: op ID `dedup.autofile-notdup-issues`, params `threshold` (int, default 5), `apply` (bool, default false), `confirm` (string, default empty). Registration mirrors the sibling exactly.
3. Dry-run path (default): scan `not_dup` labels with `LabelSource != "human"`, group into clusters keyed by the tuple `(LabelReason, SignatureRelation, FolderRelation)` — these are exact field names on `LabeledExample` in `internal/database/dedup_label.go`; confirm them via the anchor grep before use (empty field values are legal key components, e.g. `("", "unknown", "")` is one cluster). Keep clusters with suppressed-count > threshold, emit a report: cluster key, count, up to 5 sample book IDs/titles, and a deterministic SHA-256 `report_hash` over the sorted cluster keys+counts. ZERO GitHub calls on this path.
4. Apply path: require `apply=true` AND `confirm == report_hash` of a dry-run over the same data; on mismatch return an error naming the expected flow (dry-run → human AskUserQuestion review → apply with the hash). On match, create one GitHub issue per cluster (title `[dedup C8] not_dup cluster: <key> (<count> suppressed)`), recording every created issue number in the op result. **Target repo: `falkcorp/burndown-tasks` (the burndown hub — precedent: the previous dedup-cluster round was filed there as burndown-tasks #52–#67, see TODO.md:826), NOT falkcorp/audiobook-organizer.** Make the target repo a config/param (`target_repo`, default `falkcorp/burndown-tasks`) rather than a literal. Note: the burndown bot workflows are paused since 2026-07-08 (OpenAI quota) — filing is independent of the bot loops, but issues will not be auto-triaged until the bot is re-enabled. Fail-closed on ANY GitHub API error: stop, report partials, never retry-loop issue creation.
5. Cluster/book iteration uses the package's existing `RunItems` bounded pool; issue creation itself is SERIAL (rate-limit respect + ordering — say so in a comment).
6. NEW `_test.go`: (a) dry-run performs zero outward calls and populates the report; (b) `apply=true` with wrong/empty `confirm` errors and files nothing; (c) threshold happy path — a cluster ABOVE threshold IS reported (anti-over-suppression) and one below is excluded; (d) UNKNOWN-field cluster still counted.
7. Purely additive: touch no existing dedup files except (if the sibling pattern requires it) the shared op-registration list — mirror how the sibling registered itself.
8. `TODO.md`: tick C8 `[x]` with the PR number. PR body: `Closes #1447` + a note that ACTUAL issue-filing still requires the dry-run → AskUserQuestion → apply protocol.
9. Bump headers on every touched file; new files get fresh 4-line headers.

## How to test

```bash
make ci
go test ./... -short   # FULL short suite — store/op-registry changes must not vacuously pass a subset
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "dedup.autofile-notdup-issues" internal/plugins/dedup/autofile_notdup_issues.go` hits.
- [ ] Refusal test green: unconfirmed apply files ZERO issues (`grep -n "confirm" internal/plugins/dedup/autofile_notdup_issues_test.go` shows the case).
- [ ] Anti-over-suppression: above-threshold cluster IS reported (named test green).
- [ ] UNKNOWN-field cluster still counted (test green — missing metadata is non-disqualifying).
- [ ] Tests green (full `go test ./... -short`); vet/lint clean on changed files.
- [ ] File headers present/bumped on every touched file.

## Commit message

```
feat(dedup): add dedup.autofile-notdup-issues op, dry-run default + confirm-hash apply gate (C8, #1447)

Surfaces systematic false-positive sources as GitHub issues per not_dup
cluster. Outward issue creation is structurally gated: apply requires the
dry-run report hash, and dispatch requires a human AskUserQuestion decision.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ux-small-items-c8-autofile-notdup-issues
gh pr create --fill
gh pr merge <number> --rebase
```
⚠ review-critical: the coordinator must line-review the confirm-hash refusal path before merging.

## Idempotency / Rollback

If `grep -n "dedup.autofile-notdup-issues" internal/plugins/dedup/autofile_notdup_issues.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit (op unregisters; no schema/data touched). If issues were already filed by an apply run, close them via `gh issue close <n>` using the issue numbers recorded in that run's op result — the op records them for exactly this purpose.
