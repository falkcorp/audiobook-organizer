<!-- file: docs/agent-tasks/todo-completion/handoff/working-practices.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8d21b6f4-0e93-4c57-b8a2-6f15da709c33 -->
<!-- last-edited: 2026-08-23 -->

# Working practices for the TODO-completion package

Hard-won rules. Each one exists because ignoring it cost real time or shipped a
real defect.

## Gates and evidence

- **`make ci` is RED on `main` for unrelated reasons and is NEVER the gate.**
  Use the per-task `gate` field in `skeleton.json`.
- **Never pipe `go test` through `head` and read `$?`.** That reports `head`'s
  exit code, not the test's, and shows a false green. It happened in this
  session: `go test ./internal/ai/... | head -8; echo exit=$?` printed `exit=0`
  while `TestLLMParserBuild_ModeGated` was failing, and the branch went to CI
  red. Redirect to a file, echo the real exit code, then grep the file.
- **A green gate on a mechanical brief is weak evidence.** Standing instruction
  from the owner: always review subagent output.

## Mutation discipline

The recurring real defect in this package is **tests that pass for the wrong
reason**. Three separate `NEGATIVE CONTROL` comments were reasoned rather than
run, and all three were wrong.

- **COMMIT BEFORE MUTATING.** `git checkout` restores from the INDEX and will
  eat an uncommitted fix.
- **Neutralize the condition** (`stranded > 1<<30`), never delete the block — a
  build failure proves nothing.
- **Report what the mutation ACTUALLY PRINTED.** Paste it. Do not describe what
  it would print.
- **Add a positive control.** A guard that refuses everything passes every
  negative test while turning the job into a no-op.
- **Fixtures must make the two counts DISAGREE.** A fixture where the filtered
  and unfiltered counts agree passes with or without the guard.

A worked example of the failure mode, found in this session: a test named
`TestAsSeriesBookRefStore_ResolvesPebbleStore` whose doc comment correctly
described guarding against the Bleve decorator, but whose body only asserted a
bare `*PebbleStore`, `nil`, and `struct{}{}` — all of which pass against a plain
type assertion. The comment was right and the body tested something else. Fixed
in PR #2789.

## The bug family this package keeps hitting

**Filtered vs unfiltered is the whole thing.** `GetAllSeriesBookCounts`,
`GetBooksBySeriesIDCore`, `GetBooksByAuthorIDCore`, `GetAllAuthorBookCounts`
all skip `MarkedForDeletion` and non-primary rows. That is RIGHT for a display
badge and WRONG as an existence test.

Recorded production damage (`internal/database/series_bookref.go`): **6,893
phantom series IDs held by 13,322 live books + 702 trashed**, measured
2026-08-14.

Four failure shapes to check on every guard in this family:

1. **Attempted vs succeeded.** A guard dividing by `len(books)` counts rows
   ATTEMPTED. Any `continue` or error path leaves rows stranded while the guard
   passes. Count what actually completed.
2. **Fail-open scans.** A Pebble range scan that skips undecodable rows with
   `continue` and never checks `iter.Error()` returns a short map with a nil
   error. Undercounting reads as "nothing references this" — which disables the
   guard exactly when it matters.
3. **Capability lookup through the decorator chain.** Production wraps the store
   in the Bleve `indexedStore` decorator (`internal/server/indexed_store.go:99`
   `Unwrap()`). A bare type assertion against `*PebbleStore` returns nil in
   production, so the guard silently no-ops. Use `database.AsCapability`, and
   make the TEST exercise a DECORATED store — a test asserting only a bare
   `*PebbleStore` passes with a bare assertion and proves nothing.
4. **Fail closed.** A store that cannot answer the unfiltered question must
   REFUSE to delete, never fall back to the filtered count.

**And the one this session discovered last, the hard way:** hardening the Pebble
scan is not enough, because `UseMemDB` is true in production and the memdb
branch is a lossy projection. See `2026-08-23-open-findings.md` §1.

## Dispatching agents

- **Briefs are STALE** — generated at `46628240`. Every dispatch prompt gets a
  STEP ZERO: re-find anchors BY TEXT not line number, verify at HEAD, and report
  ALREADY-DONE rather than manufacture work. Two briefs were already done
  (TASK-132 by #2750, TASK-082 by `e5cf51f59`).
- **Briefs are also INCOMPLETE.** TASK-003's brief named one call site; there
  were two. TASK-029's names "three call sites at lines 344, 492, 565"; there are
  four, at different lines, and one of them MUST NOT be switched. TASK-028's
  brief named two delete guards; the agent found a third
  (`maintenance.purge-empty-authors`, the highest-volume one) by sweeping BOTH
  filtered counters instead of one. Verify the brief's scope, don't trust it.
- **Corollary:** when task B is "add a test for what task A fixes", check A
  before dispatching B.
- **Check `exact_files` collisions before dispatching.** TASK-028 and TASK-029
  share three files (`mock_store.go`, `mocks/mock_store.go`,
  `author_getter_conformance_test.go`).
- **Agents die constantly** (API errors, one "stream watchdog" stall). Memory was
  ruled out — swap stayed 0. Every prompt must say **commit AND push after every
  step**; that is what saved TASK-036 and TASK-044.
- **The scratchpad is NOT per-agent.** Two agents collided on
  `<scratchpad>/pr.md` and #2777 briefly carried the wrong task's body. Require
  `<name>-TASK-NNN.md`.
- **Prefer the repo's specialist agents** (`audiobook-organizer:go-specialist`,
  `:db-design`, `:expert`, `:schema-auditor`, plus repo-local `code-reviewer`,
  `test-runner`, `typescript-specialist`) over `general-purpose`. NOTE: they only
  register when Claude Code is started with its project root AT the repo. A
  session rooted elsewhere with the repo as an additional working directory
  cannot see them.
- `pr-review-toolkit:code-reviewer` and `:silent-failure-hunter` earned their
  keep: run in parallel on PR #2787 they independently converged on the same
  critical, and one produced a working repro probe.

## Repo conventions that bite

- **File version headers are MANDATORY** — bump `version:` and `last-edited:` on
  every file touched.
- **EXCEPT `changelog.d/` fragments, which carry NO header at all.**
  `changelog.d/README.md` documents the exemption — a header leaks into
  `CHANGELOG.md`. CI enforces a fragment on most PRs ("Require changelog
  fragment").
- **`skeleton.json` is an OBJECT** — `.tasks[]`, not `.[]`.
- **`ggrep` is NOT installed** on the owner's Mac, despite GNU tools generally
  being `g`-prefixed. Use `grep -E` / `grep -oE`.
- **Worktrees have no `node_modules`.** A bare `npx tsc` silently runs TS 5.7
  against a repo pinning ^6.0.3 and fails on `ignoreDeprecations`. Run `npm ci`
  first. `tsc --noEmit` is mandatory for any `web/` change — Vitest transpiles
  without typechecking.
- **CLAUDE.md mandates worktrees for all edits.**

## CodeQL

- **`lgtm[]` comments suppress NOTHING in this repo.** Legacy LGTM.com mechanism
  that GitHub code scanning never adopted. Proven on PR #2781: markers removed,
  comments rewritten, all four alerts stayed open across the merge. Only the
  code-scanning API dismissal closed them.
- Dismiss with
  `gh api -X PATCH /repos/falkcorp/audiobook-organizer/code-scanning/alerts/N -f state=dismissed -f dismissed_reason='false positive' -f dismissed_comment='…'`.
  `dismissed_comment` is capped at 280 bytes.
- **Resolve alerts by PATH, not by remembered number.** Dismissals have not
  always survived line shifts here (#1094 was dismissed and #1105 immediately
  reappeared for the same sink). In the #2781 merge the numbers did survive, but
  do not rely on it.
- Querying with `&ref=refs/heads/main` narrows results misleadingly. Query the
  alert number directly to check state.

## Counting merged tasks for the burndown

Map PRs to tasks via branch name `agent/<lane>-<NNN>-<slug>`:

```
gh pr list --state merged --limit 400 --json number,title,headRefName
```

- `--limit 400` silently truncates (the repo has ~2516 merged PRs). Verify the
  sample reaches below **#2682** — the package's master-plan PR, the floor for
  all todo-completion work.
- Bound the heuristic to `>= 2682` so pre-package `agent/*-NNN-` branches cannot
  create false positives. Last check: zero diff, so the heuristic is clean.
- The heuristic MISSES non-`agent/` branches. Known: TASK-132 merged via
  `docs/task-132-already-done` (#2772). One manual addition was needed to
  reconcile.
- Arithmetic must reconcile: merged + open + remaining == 208.
