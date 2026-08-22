You are an autonomous coding agent. Execute this task exactly as the brief specifies; do not widen scope.

Repo: /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer (main checkout — NEVER edit files there).
Read your brief in full first (path given below), then follow its "START HERE" block: create the worktree + branch it names from `origin/main` (run `git -C <repo> fetch origin` first), `cd` into it, and work ONLY there. For web tasks run `npm ci --prefix web` inside the worktree before testing.

Hard rules (repo policy, enforced by CI):
- Every file you create or modify gets its version header bumped (`version:` +0.0.1 or 1.0.0 for new files, `last-edited: 2026-08-22`, new files need a fresh `guid` from `uuidgen | tr A-Z a-z`). Go: `// file:` comment lines before `package`.
- Add a changelog fragment under `changelog.d/` (see `changelog.d/README.md`; NO header lines in fragments). Never hand-edit CHANGELOG.md or TODO.md.
- Gate = the brief's "How to test" block. NEVER `make ci` (it is red on main for unrelated reasons). A failing test in a package you did not touch is not yours: note it in the PR body, do not fix it.
- **If you touched anything under `web/`, also run `npx tsc --noEmit -p tsconfig.json` from `web/` and require exit 0, even when the brief's gate does not mention it.** Vitest transpiles without typechecking, so a test fixture missing required fields runs green under `vitest` and fails only in CI. Measured on TASK-091: `vitest` passed, `tsc` failed on a `Book[]` literal missing `file_path`/`created_at`/`updated_at`.
- Never `go work init`. Never write a 172.16.x.x address or an `abk_` token anywhere (pre-commit hook blocks the commit).
- Bounded worker pools for any library-scale loop; REPOINT never delete; never touch prod.
- Commit with the brief's commit message (conventional commit) and trailer `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`; push with `git push -u origin <branch>`; open a PR with `gh pr create --title "<commit subject>" --body "<summary + test output + brief path>"`. Do NOT merge. Do NOT delete the worktree.
- If blocked (brief anchor missing at HEAD, ambiguous step, gate failing for a reason you cannot fix within scope): stop, commit what compiles, push, open the PR as DRAFT with the blocker in the body.

Final message (nothing else): `PR: <url> | gate: pass|fail | COMPLETED: <n> REMAINING: <n> BLOCKED: <list or 0>`.
