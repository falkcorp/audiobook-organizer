### Added

#### A tiered census of every open TODO, and a plan to finish them

`TODO.md` had grown to 10,849 lines and nobody could say how much work was
actually left in it. It now has an answer: **385 open items**, tiered, with a
plan for retiring each tier.

The count took two tries. `TODO.md` encodes tasks two different ways — checkbox
bullets through the body, and a numbered backlog in the tail where "done" is
struck-through rather than unchecked — and the first census saw only the
checkboxes. Reconciling the 54 `todo-sync` GitHub issues against those
checkboxes suggested 24 issues were stale and closeable; spot-checking 18 of
them found 17 alive in the numbered backlog. Exactly one issue (`DOCS-1`,
#1276) is genuinely orphaned. Both mistakes are written into the plan so the
next pass does not repeat them.

The tiers: **37** items already done and just never checked off, **29** blocked
on a decision only the owner can make (several sit on production data-loss
paths), and **319** actionable. Of those 319, only 125 cite a file path
concrete enough to schedule against — the remaining 194 describe real work in
terms of symbols and behaviours, so the first wave of execution is a scoping
pass, not an implementation pass.

Also surfaced along the way: four finished commits sitting in worktrees with no
pull request, against zero open PRs on the repo.

Planning only — no code changed, and nothing was executed.
