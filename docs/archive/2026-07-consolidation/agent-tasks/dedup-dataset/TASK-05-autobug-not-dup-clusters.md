&lt;!-- file: docs/agent-tasks/dedup-dataset/TASK-05-autobug-not-dup-clusters.md --&gt;
&lt;!-- version: 1.0.0 --&gt;
&lt;!-- guid: 9e1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8 --&gt;
&lt;!-- last-edited: 2026-07-01 --&gt;

# TASK-05 — Auto-file a GitHub issue per not_dup cluster after backfill (C8)

⚠️ **DEFERRED — DO NOT START THIS TASK YET.** This task is blocked on the
prod labeled-dataset backfill completing (tracked separately as "Bucket 3" /
the CONS-10 drain sequence). Do not dispatch this task to a worker, and do not
implement or merge it, until a human has explicitly confirmed the backfill is
done. This brief exists only to capture the design ahead of time so it is
ready to execute once unblocked.

**Priority:** P3 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** blocked on prod backfill (Bucket 3 / CONS-10 drain) — coordinate with a human before starting.

## ⛔ START HERE (do this first, exactly — ONLY after backfill confirmed complete)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dd-autobug-not-dup-clusters" -b agent/dd-autobug-not-dup-clusters origin/main
cd "$REPO/.worktrees/dd-autobug-not-dup-clusters"
git rebase origin/main
```
Do all work inside that worktree. Never edit `main` or the primary checkout.

## Goal

⚠️ **Deferred design task.** After the labeled-dataset backfill has populated a
large set of `not_dup`-labeled examples in prod, design and implement an
operation that clusters those `not_dup` examples (grouping by shared failure
pattern — e.g. same rule/reason that misfired, same folder-relation type, same
signature-relation type) and files **one GitHub issue per cluster**
summarizing the pattern and linking example candidate IDs, so a human can
triage systemic false-positive sources instead of one-off complaints. Must be
gated behind an explicit flag and default to dry-run (print/log the issues it
would file, without actually calling the GitHub API) unless the flag is
explicitly set.

## Background (verify before editing — line numbers drift)

- No auto-bug-filing exists yet anywhere in this repo — confirm with:
  ```bash
  grep -rln "CreateIssue\|issues.Create\|gh issue create" internal/ | grep -v _test.go
  ```
- Look at `internal/plugins/maintenance/` for the existing op registration
  pattern (an op is a Go type registered in `internal/plugins/maintenance/register.go`
  or similar — check `internal/plugins/maintenance/register.go` and pick one
  small existing op, e.g. `internal/plugins/maintenance/orphan_book_files.go`,
  as a structural template: op struct, `Run` method, dry-run flag handling,
  progress logging).
- Labeled examples live in `internal/database/dedup_label.go`
  (`LabeledExample` struct, `ListLabeledExamples`/`LabeledExampleFilter`) —
  filter by `label_source`/label field for `not_dup` (confirm the exact label
  string constant used for "not a duplicate" by grepping
  `grep -rn '"not_dup"' internal/`).
- Check whether a GitHub client/token is already available anywhere in
  `internal/` (e.g. for release automation) that could be reused rather than
  adding a new dependency — grep `grep -rln "go-github\|github.com/google/go-github" go.mod internal/`.

Run these to confirm the current state before designing/editing:
```bash
grep -rn '"not_dup"' internal/database/ internal/dedup/
cat internal/plugins/maintenance/register.go | head -50
sed -n '1,60p' internal/plugins/maintenance/orphan_book_files.go
grep -rln "go-github" go.mod
```

## Step-by-step (design first, then implement — only after unblocked)

1. Confirm with a human that the prod labeled-dataset backfill (Bucket 3 /
   CONS-10 drain) has completed. Do not proceed past this step otherwise.
2. Decide the clustering key: group `not_dup` examples by
   `(reason, signature_relation, folder_relation)` or similar coarse tuple
   (read the actual `LabeledExample` fields to pick a sensible key — do not
   invent fields that don't exist).
3. Implement a new maintenance op (following the existing op pattern found in
   step above) that: (a) lists all `not_dup` labeled examples via
   `ListLabeledExamples`, (b) groups them by the chosen key, (c) for each
   cluster above a minimum size threshold (make this configurable, default
   e.g. 5), builds an issue title/body summarizing the pattern and up to N
   example candidate IDs (default e.g. 10), (d) in dry-run mode (the default),
   logs/prints what it *would* file without calling the GitHub API; in
   non-dry-run mode (explicit flag), files the issue via whatever GitHub
   client mechanism already exists in the repo, or a minimal new one if none
   exists — flag this explicitly in the PR description for review.
4. Add a hard-coded safety flag (e.g. an op parameter `--confirm-backfill-done`
   or similar) that must be explicitly set to `true` in addition to the
   dry-run/live flag, so the op cannot accidentally run for-real before a
   human has acknowledged the backfill precondition.
5. Bump the file header `version` and `last-edited` on every file you touch.

## How to test

Add a test that seeds several `not_dup` labeled examples with varying keys,
runs the clustering op in dry-run mode, and asserts: (a) clusters are formed
correctly by the chosen key; (b) no GitHub API call is attempted in dry-run;
(c) issue body/title content includes the cluster's example candidate IDs.

```bash
go build ./...
go test ./internal/dedup/... ./internal/database/... ./internal/plugins/maintenance/... -count=1
go vet ./internal/plugins/maintenance/...
```

## Acceptance criteria

- [ ] ⚠️ Human has confirmed the prod backfill is complete before this task was
      started (state how/where this was confirmed in the PR description).
- [ ] New op clusters `not_dup` labeled examples by a documented key and emits
      one issue (or dry-run log entry) per cluster above a minimum size.
- [ ] Defaults to dry-run; requires an explicit flag to file real issues.
- [ ] New tests cover clustering correctness and the dry-run-by-default
      behavior, and pass.
- [ ] `go test ./internal/dedup/... ./internal/database/... ./internal/plugins/maintenance/... -count=1` passes; `go vet` clean.
- [ ] File headers bumped on every changed file.

## Commit message
```
feat(dedup): auto-file GitHub issues per not_dup cluster (C8)

After the labeled-dataset backfill, not_dup examples reveal systemic
false-positive patterns that are easy to miss one-off. Add a dry-run-by-default
maintenance op that clusters not_dup examples by failure pattern and files one
GitHub issue per cluster above a minimum size, gated behind an explicit
confirm-backfill-done flag.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/dd-autobug-not-dup-clusters
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Idempotency: if the clustering op already exists and is registered, this task
is already done — verify dry-run default and tests exist and stop.

Rollback: revert the single commit; the op is additive and off-by-default
(dry-run + explicit confirm flag), so no other behavior is affected by
reverting.