<!-- file: todo.d/README.md -->
<!-- version: 1.1.1 -->
<!-- guid: 4663309b-ed2d-45f1-a6d0-7d309c62481d -->
<!-- last-edited: 2026-08-22 -->

# TODO fragments (`todo.d/`)

New tasks are **added** to `TODO.md` by dropping a small, uniquely-named
Markdown fragment in this directory. A scheduled job folds every fragment into
the `## 📥 Inbox` section of `TODO.md` and deletes the fragments it consumed.

This exists for the same reason [`changelog.d/`](../changelog.d/README.md) does:
many contributors and AI agents open PRs in parallel, and if every one of them
edited the same region of `TODO.md` directly, every PR would collide. A
fragment-per-task means no two PRs touch the same file, so there are no merge
conflicts on the TODO list.

> The changelog system uses the maintained OSS tool
> [`scriv`](https://scriv.readthedocs.io/). scriv is changelog-only and has no
> TODO equivalent, so assembly here is done by
> [`scripts/assemble_todo.py`](../scripts/assemble_todo.py). The fragment model
> is otherwise identical, deletion-on-collect included.

## Add a task

Create `todo.d/<YYYY-MM-DD>-<short-slug>.md` — the date prefix is what makes
fragments sort chronologically, and the slug is what keeps two people adding a
task on the same day from colliding:

```markdown
- [ ] **TODO-PIN** Pin all reusable-workflow `uses:` refs to commit SHAs —
      several downstream repos pin a github-common SHA that no longer exists on
      `main`, so their super-linter job cannot resolve the workflow at all.
```

Copy [`templates/new_fragment.md`](templates/new_fragment.md) for a scaffold.
There is deliberately **no PR check** for TODO fragments (unlike changelog
fragments) — adding a task is optional, not something to enforce on every PR.

## Rules

- **Add-only.** Fragments _add_ tasks. Checking a task off, deleting it, or
  promoting it out of the Inbox into a curated section is a normal direct edit
  of `TODO.md` — those are low-collision and gain nothing from fragments.
- **One fragment per logical task** (or per tight cluster of related subtasks).
- Fragments are **exempt from the file-header rule** — do not add the
  `file`/`version`/`guid` header. The body is folded into `TODO.md` verbatim, so
  a header would leak into the assembled document. They are also excluded from
  markdownlint and prettier via `.markdownlintignore` / `.prettierignore`.

  Between 2026-07 and 2026-08 this rule lived here and nowhere else, and **74
  headers leaked into `TODO.md`** — every one of them written by a contributor
  correctly obeying the org-wide "every Markdown file carries a header" rule and
  never reading this line. Documenting an exception is not enforcing it. Two
  layers now do:

  - `scripts/assemble_todo.py` **strips** a leading header block from every
    fragment body at collect time, so a fragment that has one still assembles
    cleanly.
  - `python3 scripts/assemble_todo.py --lint` fails if `TODO.md` contains a
    leaked header, and runs on every PR as `ci.yml`'s `TODO Fragment Headers`
    job. It ignores fenced code blocks, so a task that *documents* the header
    format is fine.
- A fragment that is **entirely HTML comments** is treated as an intentional
  no-op: it is deleted on collect without contributing anything. Comment out a
  fragment rather than deleting it if you want the collector to drop it quietly.

## How assembly works

- [`scripts/assemble_todo.py`](../scripts/assemble_todo.py) inserts every
  fragment body directly below the `<!-- todo-insert-here -->` marker in
  `TODO.md`, bumps that file's `version:` header, and `git rm`s the consumed
  fragments so the insertion and removals land in one commit.
- [`.github/workflows/todo-collect.yml`](../.github/workflows/todo-collect.yml)
  runs it daily and on `workflow_dispatch`, committing with `[skip ci]`.
- Configuration lives in [`todo.ini`](todo.ini). **That file's presence is what
  opts a repository in** — the assembler and the workflow both self-skip when it
  is absent, so the workflow is harmless in a repo that has not adopted this.

Run it yourself to preview:

```bash
python3 scripts/assemble_todo.py --dry-run   # print the result, change nothing
python3 scripts/assemble_todo.py --check     # exit 1 if fragments are pending
```

## Finishing work that had a fragment

A fragment can be **assembled between** the PR that files it and the PR that
finishes it. The collect job runs daily, so a task filed in the morning can
already be folded into `TODO.md` — and its fragment `git rm`ed — hours before
the PR that does the work merges. That happened on 2026-08-10 with a 26-minute
window: PR #2272 added the fragment at 04:25 EDT, the collect job consumed it
at 04:51 EDT, and PR #2273 did the work at 05:12 EDT while "deleting" a file
that was already gone. `TODO.md` was left carrying an unchecked entry for
finished work, cleaned up by hand in #2274.

**So before merging a PR that completes work which had a `todo.d` fragment:**
`grep TODO.md` for the fragment's title or text, and check the entry off by
hand if assembly got there first. Do not treat your own PR's deletion of the
fragment as doing that for you — the collector always deletes fragments on
fold-in, so a missing fragment looks identical whether the task was assembled
or never existed.

This is a rule, not a script, and it is worth knowing why before trying to
automate it. "A PR that deletes a fragment must check off the matching entry"
misses exactly the case above, because after a rebase the deletion is not in
the PR's diff at all. "Flag any `- [ ]` entry whose fragment is missing"
matches _every_ assembled entry, since assembly always deletes. Neither
direction can tell whether the work actually happened, and that is not
derivable from the files. The least-bad mechanical option is a check on the
**PR body** — a PR that says it closes a `todo.d` fragment, or deletes one,
must also touch `TODO.md` — and if it is ever added it should be written as a
heuristic, because it structurally cannot be a guarantee.
