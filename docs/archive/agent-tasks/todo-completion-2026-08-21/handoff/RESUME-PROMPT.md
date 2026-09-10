<!-- file: docs/agent-tasks/todo-completion/handoff/RESUME-PROMPT.md -->
<!-- version: 1.0.0 -->
<!-- guid: c47e9b02-1a86-4f3d-95c7-b2e0417d8aa9 -->
<!-- last-edited: 2026-08-23 -->

# Resume prompt

**Start Claude Code with its project root AT this repo**
(`cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer && claude`), not
from another directory with this repo added as an extra working dir. The
specialist agents (`audiobook-organizer:go-specialist`, `:db-design`,
`:expert`, `:schema-auditor`, `:docs-agent`, `:pii-scanner`, plus repo-local
`code-reviewer`, `test-runner`, `typescript-specialist`) only register when the
project root is this repo. A session rooted elsewhere silently falls back to
`general-purpose`.

Then paste everything below.

---

Resume the `docs/agent-tasks/todo-completion/` package (208 briefs).

Read these three files first — they are the handoff:

- `docs/agent-tasks/todo-completion/handoff/2026-08-23-open-findings.md` —
  blocking findings, including one that affects already-merged code
- `docs/agent-tasks/todo-completion/handoff/working-practices.md` — the rules
  that keep this package from shipping defects; read before dispatching anything
- `docs/agent-tasks/todo-completion/handoff/2026-08-23-state.md` — PR-by-PR
  state as of the handoff

The task list is `docs/agent-tasks/todo-completion/skeleton.json`. It is an
OBJECT — `.tasks[]`, not `.[]`.

## Do these in order

**1. Land what is already green.** Check every open PR in the package. Review
before merging — standing policy approved 2026-08-22, never ask again — then
`gh pr merge --rebase`. Use `pr-review-toolkit:code-reviewer` and
`pr-review-toolkit:silent-failure-hunter` in parallel for anything non-trivial;
on PR #2787 they independently converged on the same critical and one produced a
working repro.

**2. Fix the memdb fail-open. This is the most important item.** See
open-findings §1. Both `GetAllSeriesBookRefCounts` and
`GetAllAuthorBookRefCounts` dispatch to the memdb when `UseMemDB` is true — which
it always is in production — so the `iter.Error()` hardening that PR #2782
merged, and that PR #2787 proposes, never runs where it matters. Warmup drops
undecodable rows silently, so the guard answers "referenced by nothing" when the
truth is "could not read". Confirmed by probe, repro in the doc.

This needs fixing on BOTH sides. The series half is already merged and is
carrying the defect now.

**3. Unblock PR #2787** (TASK-028, author ref-count guard). Two confirmed
blocking criticals plus a HIGH and an IMPORTANT — see open-findings §1–4. Do not
merge as-is. Its changelog currently claims a safety property that does not hold
on the production path.

**4. TASK-029 is staged and ready to dispatch** once #2787 merges — they share
three files. A prepared dispatch prompt with the corrected call-site analysis is
in `docs/agent-tasks/todo-completion/handoff/2026-08-23-state.md`. Two things
that prompt MUST carry, because the brief gets them wrong: the brief names three
call sites and there are four (one of which is a display path that must NOT be
switched), and the `stranded` guard will stop firing naturally once the
all-versions getter lands — that is intended, not dead code.

**5. Rewrite TASK-084 before anyone runs it.** Its premise (adding `lgtm[]`
CodeQL suppressions) is empirically false in this repo — see open-findings §6.

**6. TASK-083 goes back to partially-done.** #2781 merged but resolved only two
of four findings; the two left open are real bugs, not false positives. See
open-findings §7.

## Standing rules

- Review before merging, always. A green gate on a mechanical brief is weak
  evidence.
- Every dispatch prompt gets a STEP ZERO: re-verify at HEAD, re-find anchors BY
  TEXT not line number, report ALREADY-DONE rather than manufacture work.
- Every dispatch prompt says **commit AND push after every step** — agents die
  mid-task here constantly.
- Check `exact_files` collisions before dispatching in parallel.
- `make ci` is RED on main for unrelated reasons and is NEVER the gate.
- Prefer the repo's specialist agents over `general-purpose`.

## Held by the owner, do not dispatch

TASK-046 and TASK-086 stay held. TASK-086 blocks TASK-072.
