<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-084-add-codeql-specific-lgtm-suppressions-for-the-3-.md -->
<!-- version: 2.0.0 -->
<!-- guid: d80aba87-7684-4893-b1c0-b3b7d4348862 -->
<!-- last-edited: 2026-08-23 -->

# TASK-084 — Dismiss the 3 already-justified go/disabled-certificate-check alerts via the code-scanning API (SEC-CODEQL-BACKLOG)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · misc-go subagent · **Why:** The risk judgment is already done and recorded in `#nosec` comments; what is missing is a suppression mechanism that actually works. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 2595 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-CODEQL-BACKLOG**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

> ## ⚠️ THIS BRIEF WAS REWRITTEN 2026-08-23 — v1's premise was empirically FALSE
>
> v1 told you to add `// lgtm[go/disabled-certificate-check]` comments. **`lgtm[]`
> suppresses nothing in this repository.** It is the legacy LGTM.com mechanism,
> which GitHub code scanning never adopted.
>
> Measured twice, independently:
>
> 1. On PR #2781 the `lgtm[]` markers were REMOVED and the comments rewritten.
>    All four affected alerts stayed open across the merge. Only a
>    code-scanning API dismissal closed #1429 and #1105.
> 2. Directly, on 2026-08-23: `internal/audiobooks/service_mutation.go:63`
>    carries `// lgtm[go/path-injection]` on that exact line **today**, and
>    alert **#1104**, whose location is that exact `path:line`, is still
>    `open`. Marker present, alert open. Reproduce with:
>    ```bash
>    grep -n 'lgtm\[' internal/audiobooks/service_mutation.go
>    gh api /repos/falkcorp/audiobook-organizer/code-scanning/alerts/1104 -q '.state, .most_recent_instance.location.path'
>    ```
>
> Running v1 as written would have shipped three inert comments and marked the
> findings "handled" while they stayed open — strictly worse than leaving them
> alone, because the next person would trust the marker. **Do not add `lgtm[]`
> comments. Do not treat a green build as evidence a finding is suppressed.**

## Goal

The three `go/disabled-certificate-check` alerts each already carry a written
justification in code. Confirm each justification **still holds at HEAD**, then
dismiss the alert through the code-scanning API — the only mechanism in this
repo that actually changes an alert's state.

Verified open as of 2026-08-23:

| Alert | Location | Context |
|-------|----------|---------|
| **#379** | `internal/mtls/provisioning.go:142` | Production code. The one that matters — the item's own framing ranks it above the other two. |
| **#974** | `tools/cmd/merge-split-books/main.go:93` | Operator-run one-off tool, not server code. |
| **#959** | `tools/cmd/reconcile-paths/main.go:117` | Operator-run one-off tool, not server code. |

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch anything before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-084-dismiss-disabled-cert-check-alerts" -b agent/misc-go-084-dismiss-disabled-cert-check-alerts origin/main
cd "$REPO/.worktrees/misc-go-084-dismiss-disabled-cert-check-alerts"
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

## STEP ZERO — re-verify at HEAD before doing anything

Briefs in this package were generated at commit `46628240` and are STALE. Line
numbers drift and some tasks are already done.

1. **Re-find every anchor BY TEXT, never by line number.** A zero-hit grep means
   STOP and report, not "search harder".
2. **Confirm the three alerts are still open and still at these locations.**
   Alert numbers have NOT always survived line shifts in this repo (#1094 was
   dismissed and #1105 immediately reappeared for the same sink), so resolve by
   PATH first and treat the number as a lookup key, not an identity:
   ```bash
   gh api '/repos/falkcorp/audiobook-organizer/code-scanning/alerts?state=open&per_page=100' --paginate \
     -q '.[] | select(.rule.id=="go/disabled-certificate-check") | "\(.number)\t\(.state)\t\(.most_recent_instance.location.path):\(.most_recent_instance.location.start_line)"'
   ```
   If a number here disagrees with the table above, **the live API wins** — use
   what you measured and say so in your report.
3. If all three are already `dismissed`, report **ALREADY-DONE** with the
   evidence and stop. Do not manufacture work.

## Background (verify before acting)

Re-verify these by text; a zero-hit grep means STOP:

```bash
grep -n 'InsecureSkipVerify' internal/mtls/provisioning.go
grep -n -B4 'InsecureSkipVerify' tools/cmd/merge-split-books/main.go tools/cmd/reconcile-paths/main.go
```

`provisioning.go` already has a detailed `#nosec G402` justification
("bootstrap-only: InsecureSkipVerify is required during initial mTLS cert
provisioning..."). Both `tools/cmd` sites already carry `//nolint:gosec`.

**Read the justification, do not just confirm it exists.** This repo has been
bitten by a stale justification outliving its reason (see CLAUDE.md's
2026-08-18 worked example: a comment claimed two call sites required a
398-method interface; both had been narrowed earlier and nothing re-checked the
comment). Your job is to verify the CLAIM, clause by clause — not the presence
of a comment.

## Step-by-step

1. Read the full `#nosec` comment in `internal/mtls/provisioning.go` and verify
   each clause independently:
   - "bootstrap-only" — trace the callers. Is this code path reachable outside
     first-install today? Use `findReferences` / `incomingCalls`, not grep.
   - "single use" — is it still called once?
   If **any** clause no longer holds, do NOT dismiss. Escalate it as a real
   finding and report; that outcome is a success for this task, not a failure.
2. Do the same, more briefly, for the two `tools/cmd` sites. The bar there is
   only "is this still an operator-run one-off, not reachable from the server
   binary" — confirm neither is imported by `cmd/` or `internal/`.
3. For each alert whose justification holds, dismiss it:
   ```bash
   gh api -X PATCH /repos/falkcorp/audiobook-organizer/code-scanning/alerts/379 \
     -f state=dismissed \
     -f dismissed_reason="won't fix" \
     -f dismissed_comment='Bootstrap-only mTLS provisioning; InsecureSkipVerify is required before a trusted cert exists. Justified in-code with #nosec G402. Re-verified 2026-08-23 (TASK-084).'
   ```
   - `dismissed_comment` is **capped at 280 bytes** — count them.
   - `dismissed_reason` must be one of `false positive`, `won't fix`,
     `used in tests`. For these three, `won't fix` is correct: the behaviour is
     intentional and justified, NOT a mis-detection. Do not use
     `false positive` — CodeQL is right that the check is disabled.
4. **Read each alert back** and confirm `state == "dismissed"`. An accepted
   PATCH is not proof; this repo has seen a dismissal reappear.
5. If the code comments are worth improving while you are there (e.g. the
   `tools/cmd` sites lack a one-line note that they are operator-run), that is
   in scope as a comment-only edit — but it is **not** the suppression
   mechanism and must not be described as one.

Then, always:
- Bump the file header (`version` + `last-edited: 2026-08-23`) on every file you
  touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- **Commit AND push after every step.** Agents die mid-task in this package
  constantly (API errors, watchdog stalls); an unpushed commit is a lost step.
- Use a scratchpad file named `<your-name>-TASK-084.md` — the scratchpad is
  NOT per-agent and two agents have already collided on a shared `pr.md`.
- If you changed any Go file, add a changelog fragment
  `changelog.d/20260823_misc-go_084.md` (**NO file header** — fragments are
  exempt; a header leaks into `CHANGELOG.md`). If you only dismissed alerts and
  touched no code, no fragment is needed; say so in your report so the
  coordinator can expect the "Require changelog fragment" check to need a
  `[skip changelog]` title marker.
- Do NOT edit `TODO.md` — the coordinator closes the source item. In your final
  report, state the exact `TODO.md` line text to check off.

## Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the bootstrap-only claim no longer holds for `provisioning.go`, do NOT
  dismiss #379. Escalate as a real finding.
- If an alert number no longer resolves, or resolves to a different path, do NOT
  guess. Re-resolve by path and report the discrepancy.
- If you cannot authenticate to the code-scanning API, STOP and report. Do not
  fall back to adding comments — that is exactly the inert outcome this rewrite
  exists to prevent.

## Tests

N/A — no behaviour change. If step 5 produced comment-only edits:

```bash
go build ./... && go vet ./...
```

Do NOT use `make ci` as the gate: it is red on `main` for pre-existing reasons
unrelated to this task. A failing test in a package you did not change is not
yours — report it, do not fix it.

## Acceptance criteria

- [ ] Each of the three justifications was verified clause-by-clause at HEAD,
      with the evidence quoted in the report (not "the comment says so").
- [ ] `gh api .../code-scanning/alerts/379 -q .state` returns `dismissed`
      (likewise #974, #959) — **read back after PATCHing**, do not trust the
      PATCH response.
- [ ] `dismissed_reason` is `won't fix`, not `false positive`.
- [ ] Zero `lgtm[` markers were added. `git diff | grep -c 'lgtm\['` returns 0.
- [ ] Any escalated finding (a justification that no longer holds) is reported
      with its evidence rather than dismissed.
- [ ] File headers bumped on every changed file, if any.
