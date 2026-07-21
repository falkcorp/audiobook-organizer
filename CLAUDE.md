<!-- file: CLAUDE.md -->
<!-- version: 4.11.0 -->
<!-- guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f -->
<!-- last-edited: 2026-07-21 -->

# CLAUDE.md

This is an **audiobook organizer** — Go backend + React/TypeScript frontend.

## Coding Standards

Org-wide coding standards are in the `.standards/` git submodule (cloned from `https://github.com/falkcorp/.github`).
Always clone with `git clone --recurse-submodules` so these are available.

Key files:
- **File headers (MANDATORY):** `.standards/instructions/file-headers.md`
- **Go rules:** `.standards/instructions/go.md`
- **TypeScript rules:** `.standards/instructions/typescript.md`
- **Commit format:** `.standards/instructions/commit-messages.md`

## Concurrency — Prefer Multi-Core Design (MANDATORY)

Any loop that iterates a whole-library-scale collection (books, book files, dedup
candidates, authors — hundreds/thousands+ items) and does meaningful per-item work
(a DB read/write, network call, hashing/fingerprinting, fuzzy-string comparison, or
subprocess call) **must** be written with concurrency in mind from the start, not
bolted on later. A `dedup.full-scan` run went silent for 3+ hours at 100% CPU on a
single core on 2026-07-05 because its "unified scoring" pass was a plain
`for range books` loop with no worker pool — see
[`docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`](docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md)
for the full list of similar hotspots found across the codebase and the fix patterns
that apply to each shape of problem.

- Default to a bounded worker pool (`errgroup.Group` + `SetLimit`, or a semaphore
  channel) sized to `runtime.NumCPU()` for CPU-bound work, or a smaller fixed
  concurrency for network-bound work that respects the target's own rate limits.
  Never fan out unbounded goroutines over an unbounded collection.
- If the loop has a pairwise/O(n²) shape (comparing every item against every other
  item — e.g. signature/fingerprint similarity scans), shard the outer loop across
  workers rather than parallelizing the inner loop alone; watch for shared mutable
  state (a dedup-key map, etc.) that needs its own lock or sharding.
- If the loop's correctness depends on strict ordering or exclusive access (e.g. an
  auto-merge/auto-resolve apply path that must not double-merge a book processed by
  two workers at once), don't naively add concurrency — partition the work into
  disjoint sets (by book ID, group ID, etc.) so parallel workers can never touch the
  same row, and say so in a comment.
- When adding a new full-library maintenance/backfill op, look for an existing
  parallel sibling first (e.g. `internal/plugins/acoustid/backfill.go`'s
  `registry.RunItems`-based pattern) before writing a new sequential loop from
  scratch — several serial hotspots in the audit above are sequential duplicates of
  an already-correctly-parallelized twin elsewhere in the codebase.

## Worktree Discipline (MANDATORY)

- **NEVER** edit files directly in the main working tree
- **ALWAYS** create a worktree + feature branch before any code changes:
  `git worktree add ../<repo>-<feature> -b <branch-name>`
- **NEVER** commit directly to main — all changes go through PRs
- If you catch yourself editing main, **STOP immediately**, move changes to a worktree, and reset main

**Before any edit:** run `git worktree list` to confirm current location. If in the primary checkout (main), use EnterPlanMode (which enforces worktree creation) or manually create a worktree first.

**After merging:** always remove the worktree immediately after the PR merges:
```bash
git worktree remove .worktrees/<branch>   # or the full path
git worktree prune                         # cleans up any stale refs
```
Never leave merged worktrees sitting around — `git worktree list` should stay short.

**Why:** This repo has production at stake. Direct commits to main conflict with concurrent work. This is non-negotiable — no exceptions.

## Plan Before Execution

- For any refactor, migration, or multi-file change, present a **written plan FIRST** and wait for approval
- Write the plan to `PLAN.md` at the worktree root, covering: goal / files to change / ordered steps / test strategy / rollback
- Do **not** start exploring/editing files until the user confirms the plan
- Use TodoWrite to capture the plan visibly

## Status Reporting Honesty

- When reporting completion, give **EXACT counts** (e.g., `33/41 fixed, 8 remaining`) — never aggregate claims like "all done"
- If a subagent is still running, explicitly state "X subagents still in progress"
- Every status update must end with:
  - `COMPLETED: <count> — <list>`
  - `REMAINING: <count> — <list>`
  - `BLOCKED: <count> — <list>`
- **Never** use "all done", "complete", or "finished" without a number backing it up
- Never claim "all complete" until you have verified every item in the original scope

## Parallel Subagent Coordination

- Never launch overlapping waves (e.g., W9b + W9c on related files) in parallel — they will conflict on rebase
- Subagents must report progress every 5 minutes; if silent >10 min, surface the delay to the user
- Before enabling feature flags in production, verify data backfill has completed

## Quick Start

- **Architecture & workflows:** [.github/copilot-instructions.md](.github/copilot-instructions.md)
- **Coding standards:** [.github/instructions/](https://github.com/falkcorp/audiobook-organizer/tree/main/.github/instructions/)
- **Prompts:** [.github/prompts/](https://github.com/falkcorp/audiobook-organizer/tree/main/.github/prompts/)
- **Full file index:** [AGENTS.md](AGENTS.md)

## Build & Test Commands

The Go binary embeds the React frontend via `//go:embed web/dist` (build tag
`embed_frontend`). Frontend must be built first — use `make` for everything.

```bash
make build           # Full build: npm install + npm run build + go build (embedded UI)
make build-api       # Backend only, no frontend (quick iteration)
make run             # Full build then serve
make run-api         # API-only build then serve
make test            # Go backend tests
make test-all        # Backend + frontend tests
make test-e2e        # Playwright E2E tests
make ci              # Fast CI: mocks/staticcheck/short tests + 30% coverage gate (coverage-check-short)
make web-dev         # Vite dev server (frontend only)
make help            # All targets
```

> **Note:** `go.mod` currently says `go 1.24.0`. The Go instructions reference 1.25 features — update go.mod when upgrading.

## Setup: Git Pre-Commit Hook & Credentials Management

### Pre-Commit Hook (One-Time Setup)

Protect against accidentally committing auth credentials:

```bash
bash scripts/setup-git-hooks.sh
```

This installs a pre-commit hook that blocks commits of:
- `.api-token` — shared API key across worktrees (created by `server-bootstrap` skill)
- `.bootstrap-token` — temporary bootstrap auth token
- `.claude/.credentials/` — per-worktree usernames/passwords

### Per-Worktree Credentials

Each worktree can have its own credentials (username/password) for isolated access:

```bash
# Create credentials for current branch (auto-generates username from branch name)
./scripts/manage-credentials.sh create

# Create for a specific branch
./scripts/manage-credentials.sh create fix-auth

# List all stored credentials
./scripts/manage-credentials.sh list

# Get credentials for current branch
./scripts/manage-credentials.sh get

# Show how to use in curl
./scripts/manage-credentials.sh use

# Delete credentials
./scripts/manage-credentials.sh delete

# Clean up all credentials
./scripts/manage-credentials.sh cleanup
```

Credentials are stored in `.claude/.credentials/<branch-name>.json` and are .gitignored.
Username auto-generates from branch name (e.g., `fix-auth` → `claude_fix_auth`). Password is generated once and stored securely.

## Workflow Discipline

- ALWAYS use a git worktree for refactors and multi-PR work; never commit directly to main in the primary working tree. Check `git worktree list` first; if in the main checkout, create `git worktree add ../<repo>-<branch> -b <branch>` and confirm the path back before any edits.
- ALWAYS present a written plan (in `PLAN.md` at the worktree root) covering goal / files to change / ordered steps / test strategy / rollback BEFORE exploring code or making edits. STOP and wait for explicit approval.
- Before running any multi-step build, deploy, or reset sequence, run `grep -E '^[a-z-]+:' Makefile Makefile.local 2>/dev/null` to list targets. Prefer an existing target over manual commands, and state which target is being used and why.
- For ≥3 mechanically-similar refactor tasks, use the `/parallel-sweep` slash command — it handles worktree-per-task isolation, autonomous PR + admin-merge with the local-`make ci` gate, sibling-rebase loop with Sonnet/Opus conflict resolvers, and resume across usage limits. Spec: [`docs/superpowers/specs/parallel-sweep.md`](docs/superpowers/specs/parallel-sweep.md).
- For any design doc, plan, or review longer than ~300 lines, write it to a file under `docs/` (shared) or `.claude/notes/` (scratch) and respond with just a summary + file path. Do NOT inline long content in chat.
- For parallel investigation: use read-only agents first, present findings, await implementation approval before any agent edits files.

## Prompts & Patterns

Use these verbatim to enforce the above disciplines in new sessions:

**Pre-deploy check:**
> Before we deploy, run a pre-deploy check: (1) list all feature flags being enabled in this PR, (2) for each, verify the underlying data source is populated in prod, (3) grep for `//go:embed` in changed files and confirm those files exist in the build context, (4) run the build locally end-to-end. Report findings before I approve deploy.

**Plan-first gate:**
> Before touching any files, produce a written plan with: (1) goal, (2) files you'll change, (3) order of operations, (4) test/verification strategy, (5) rollback plan. Write it to a markdown file and wait for my approval. Do NOT read code beyond what's needed to draft the plan.

**Status reporting:**
> From now on, every status update must end with three lines: 'COMPLETED: \<count\> - \<list\>', 'REMAINING: \<count\> - \<list\>', 'BLOCKED: \<count\> - \<list\>'. Never use words like 'all done', 'complete', or 'finished' without a number backing them up.

**Read-only parallel investigation:**
> Use 3 parallel agents to investigate (read-only) the call sites of \<X\> across the codebase. Each agent reports findings. Do NOT edit. I will review the findings before authorizing implementation.

## GitHub Operations

- Do NOT push workflow file changes via the MCP contents API — it has caused file corruption. Use git push instead.
- Pin all GitHub Action references to SHAs, not tags.
- Prefer HTTPS remotes over SSH if SSH key issues arise.

## Quick Fix Workflow

When making small fixes while another Claude process is working on main, use this
workflow to avoid conflicts. Do not ask for confirmation at each step — run through
the entire sequence:

1. `git checkout -b fix/<description>` (from main)
2. Make the fix, commit with conventional commit message
3. `git push -u origin fix/<description>`
4. `gh pr create --title "..." --body "..."`
5. `gh pr merge <number> --rebase` (this repo uses rebase/FF only, no squash)
6. `git checkout main && git pull`
7. Update the worktree: `cd <worktree> && git fetch origin main && git rebase origin/main`

## Critical Rules

1. **Git:** Use MCP GitHub tools first. Native git as fallback. Conventional commits mandatory.
2. **File headers:** All files need versioned headers. Bump version on every change.
3. **Docs:** Edit files directly. Update version headers. No legacy doc-update scripts.
4. **Scripts:** Python for anything non-trivial. Shell only for simple ops under 20 lines.

## Post-Task Hygiene

- After completing any feature/fix: update CHANGELOG, update TODO, **and check the
  executive-summary criteria** (`docs/process/executive-summaries.md`), then commit before
  moving on. Do all three together — don't treat the executive summary as a separate,
  deferrable step.
- **Why a third check, not just two:** CHANGELOG and TODO are written for engineers — file
  paths, function names, PR numbers. Most users find that gibberish. The executive summary
  (`docs/executive-summaries/`) is the one written for them: plain language, no jargon, what
  changed and why it mattered. A data-loss fix isn't done, from a user's point of view, until
  that's updated too.
- The criteria (full list in `docs/process/executive-summaries.md`): it fixes something that
  could have silently caused data loss or corruption; it spans multiple files/PRs or one PR
  with a wide blast radius; it closes out a tracked set of issues; or the user signed off on a
  multi-step plan that got executed to completion. If it qualifies, update the **current
  month's** summary in `docs/executive-summaries/` in the SAME PR as the CHANGELOG/TODO edit —
  not a follow-up PR, not "later." If it doesn't qualify (a typo fix, a single small change),
  skip it — that's what CHANGELOG/TODO are for.
- When editing release notes, PREPEND to existing auto-generated content; never replace the body wholesale.


## 📝 Changelog & TODO — Use the Fragment System (MANDATORY)

**Do not hand-edit `CHANGELOG.md`, and do not add new tasks straight into the
`TODO.md` inbox.** Both files are assembled from per-change fragments so that
parallel PRs never collide on them.

- **`CHANGELOG.md` is assembled, not hand-edited.** Add a fragment under
  `changelog.d/` (run `scriv create`, or write the Markdown file by hand). The
  fragments are folded into `CHANGELOG.md` at release time by `scriv`, and a CI
  check (`changelog-check.yml`) requires one on each PR. See `changelog.d/README.md`.
- **New `TODO.md` tasks are added via fragments.** Drop a Markdown fragment in
  `todo.d/` (see `todo.d/README.md`) instead of editing the `## 📥 Inbox`
  section. `scripts/assemble_todo.py` folds fragments in daily. This is
  **add-only**: checking a task off or removing it is a normal direct edit of
  `TODO.md`.
