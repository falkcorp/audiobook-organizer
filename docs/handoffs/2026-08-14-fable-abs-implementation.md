<!-- file: docs/handoffs/2026-08-14-fable-abs-implementation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4a92c8e1-6b70-4d35-9f28-1c05e7b3da64 -->
<!-- last-edited: 2026-08-14 -->

# Handoff — 2026-08-14 — finish the AudiobookShelf implementation (Fable session)

This handoff exists to point at one file and to record the repo state around it. It is
deliberately short: the actual instructions are not duplicated here, because a summary of
them is exactly how this task gets mis-scoped.

## The prompt to run

Everything you need is in **[`.github/prompts/abs-implementation-completion.prompt.md`](../../.github/prompts/abs-implementation-completion.prompt.md)**.

Read that file in full, **including the preamble above the horizontal rule**. The preamble
explains why the prompt is shaped the way it is; skipping it is how a session ends up
rebuilding features that already exist. Then execute the section after the `---`.

Do **not** re-scope the work from this handoff, from memory, or from
[`docs/audits/2026-08-11-abs-coverage-gap-audit.md`](../audits/2026-08-11-abs-coverage-gap-audit.md).
That audit is **stale in at least four places** — N-1 through N-4 all changed on 2026-08-12,
one of them retracted outright — and the prompt exists specifically because acting on it as
written reintroduces a bug that broke 46 live routes twice. Phase 0 of the prompt is a
mandatory ground-truth pass, and its output is what scopes every later phase.

One point is worth repeating here because it is the single most common way this task is
wasted: **three of the five features in the obvious framing are already implemented.**
Bookmarks have full CRUD; position and read status live in `progress.go`. Phase 0 tells you
which two are actually missing.

## Repo state at the time of writing

- `main` at `208df09f`. **Zero open PRs.** Working tree clean.
- No worktrees from the authoring session remain — everything it produced is merged to
  `main`, so a fresh clone or worktree sees all of it.
- `audiobook-organizer-dryrun` [`fix/maintenance-job-dryrun-default`] belongs to another
  live session. Leave it alone.
- Peer sessions have been active in this repo all day. Run `gh pr list` **and read the
  titles** before starting anything — not just `git worktree list`. That check is what
  prevents duplicating a peer's in-flight work; skipping it has already caused one
  duplicated pair of PRs.

### Known loose end, not addressed here

`internal/db/migrations/011_itunes_import_support.sql` is untracked in `main`. It is an
`ALTER TABLE audiobooks` against a table that exists nowhere in this Pebble-primary repo.
Deleting it is almost certainly correct but it is an owner call, so it was left in place
rather than committed or removed.

## Standing rules that bite hardest on this task

- **Worktree first.** Never edit the primary checkout, never commit to `main`, PRs only,
  rebase/FF merges.
- **Do not run `go work init` in a worktree.** It breaks the build: workspace mode re-admits
  the pre-split monolithic `genproto` alongside the split modules and `go build ./...` fails
  with ambiguous imports. Tolerate the gopls "not in your workspace" noise and verify with
  real `go build` / `go test`.
- **Never `git stash`.** The stash is shared across every worktree, and other live sessions
  have entries in it.
- **`todo.d/` and `changelog.d/` fragments are headerless** — exempt from the file-header
  rule. A header in a fragment lands as four lines of comment noise in the middle of
  `TODO.md`. Every other file needs its version header bumped and `last-edited` updated.
- Status reports end with `COMPLETED: <n>` / `REMAINING: <n>` / `BLOCKED: <n>`. Never "all
  done" without a number behind it.
- **A green test proves nothing until you have watched it go red.** Mutate the fix, confirm
  the test fails, restore, confirm green.

  This last one applies with unusual force to the ABS work, and the authoring session hit it
  earlier the same day: reverting a fix in the real store left an entire package's tests
  green, because that package exercised the code only through fakes and could not observe
  the real implementation at all. Had it shipped, a maintenance job would have processed
  nothing and reported success. When a package tests through a double, the double is the
  only thing under test — put the assertion where the real implementation runs, and make the
  double mirror the real semantics.
