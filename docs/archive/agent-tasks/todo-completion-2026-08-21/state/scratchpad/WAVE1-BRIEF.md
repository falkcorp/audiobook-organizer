# WAVE 1 — Tier A close-out (single agent, one commit)

Repo: /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer. You MUST work in a worktree:
```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer && git fetch origin
git worktree add .worktrees/wave1-tierA -b chore/todo-tier-a-closeout origin/main
cd .worktrees/wave1-tierA
```
Input: the candidate list in TIERA_ITEMS (path given in prompt). Each candidate is a TODO.md item that a keyword heuristic flagged as "already done" (sits under a ✅/FIXED/SHIPPED heading, or is self-marked done). The heuristic is a HYPOTHESIS.
For EACH candidate:
1. Read the item in TODO.md (grep by a distinctive phrase; line numbers drift).
2. Verify against HEAD with at least one concrete grep/git command proving the work exists (symbol present, test present, commit in `git log --oneline origin/main`, file exists). Record the command.
3. If PROVEN done: checkbox items → change `- [ ]` to `- [x]`; numbered-backlog items (`N. **ID**`) → wrap the first line's title in `~~ ~~` as the file already does for done entries. Append ` — closed 2026-08-21: <one-line evidence>` to the first line.
4. If NOT proven: leave it untouched and list it in your report as "kept open: <why>".
Do NOT touch any other line of TODO.md. Do NOT add new tasks. Bump TODO.md's header (`<!-- version:` minor bump, `<!-- last-edited: 2026-08-21 -->`).
Also: check GitHub issue #1276 (`gh issue view 1276`) — the plan says its TODO id DOCS-1 has no live TODO item; grep `DOCS-1` across TODO.md; if absent and the issue is stale, report that (do NOT close the issue yourself).
Add a changelog fragment: `changelog.d/20260821_tier_a_closeout.md` (NO file header) containing a `### Changed` bullet: "TODO: closed N stale items verified against HEAD (2026-08-21 master-plan Wave 1)". Check changelog.d/README.md for the exact fragment format first.
Commit (one commit): `chore(todo): close N Tier A items verified done against HEAD` with body listing the lines, and trailer lines:
Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
Do NOT push. Report: COMPLETED: n closed — list of L-numbers; REMAINING: n kept open — list with reasons; BLOCKED: n.
