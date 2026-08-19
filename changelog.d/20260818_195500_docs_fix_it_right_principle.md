### Changed

#### `CLAUDE.md` records the scope-vs-depth distinction

Neither `CLAUDE.md` nor the global instructions said anything about how deep to go on
a problem, and the closest guidance in memory — a "surgical precision" note — read
"Touch only what was explicitly asked. Do not expand scope, fix adjacent issues, or
'clean up while you're in there.'"

That rule is about *scope*, but it was being applied to *depth*: it justified making
the smallest change to the assigned problem rather than the correct one. The two are
now separated explicitly.

- **Scope** — stay on target. Unchanged.
- **Depth** — on that target, apply the correct fix, not the smallest one. Refactoring
  and call-site churn are an acceptable price. Never pre-emptively discount a fix as
  "cosmetic", "mechanical", or "not worth the churn".

The section includes the 2026-08-18 worked example and one further rule that came out
of it: **when a comment explains why something must stay wide, verify the claim before
believing it.** Two were checked that day and both were stale.
