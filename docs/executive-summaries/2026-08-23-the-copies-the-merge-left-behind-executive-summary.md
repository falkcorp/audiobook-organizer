<!-- file: docs/executive-summaries/2026-08-23-the-copies-the-merge-left-behind-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e2a4c98-3d61-4f05-b8a7-52c9e10d6b34 -->
<!-- last-edited: 2026-08-23 -->

# The copies the merge left behind

**2026-08-23 — when two duplicate series were merged into one, every alternate copy of a
book in them was left pointing at a series that had just been deleted**

**PR:** [#2821](https://github.com/falkcorp/audiobook-organizer/pull/2821) · merge commit
pending
**Direct sequel to:** [The series that vanished from under 13,322
books](2026-08-14-the-series-that-vanished-from-under-13322-books-executive-summary.md)
(Aug 14) — that report fixed the *guard* that decides whether a series is safe to delete.
This one fixes the step just before it: the loop that is supposed to move the books out
first.

---

## The short version

- The library often ends up with the same series listed twice under slightly different
  names. Merging them is meant to be a safe, reversible tidy-up: move every book from one
  series into the other, then delete the now-empty one.
- It did not move every book. It moved every book **it could see** — and it was asking a
  question that deliberately hides some of them.
- What it could not see were **alternate copies**: the second and third rips of a book you
  already own, which the library hides behind the main copy so your shelves are not full
  of duplicates. Those were left behind and the series was deleted out from under them.
- The same blind spot silently skipped a *write* on a third path. When two authors were
  merged, the alternate copies never received the merged author credits, and nothing ever
  goes back to check.
- This is the second time this month the same mistake has surfaced in the same corner of
  the app. A companion report on Aug 14 measured the damage the *first* one caused: books
  in the library pointed at **21,190 different series when only 14,626 existed** — 6,893
  that had been erased, still named by 13,322 books on the shelves.
- **That figure is not damage from this bug.** It is what the same mistake did on a
  different job that *was* running nightly. The scheduled job fixed here has never once
  run in production — a prior measurement recorded on our task list puts it at 0 of 10,161
  operations — so what is being repaired here is a trap that had not yet been sprung, not
  a mess already made. That measurement is quoted from that earlier note and was not
  re-taken for this report.
- The fix adds a second, complete way of asking "what is in this series?" and switches the
  three places that move books before a delete over to it, leaving the one display-only
  place on the original.
- The regression is now blocked by a test written against the *pair of questions*
  themselves rather than against a list of places that ask them — which matters, because
  the task that commissioned this work listed three such places and there were four.

---

## 1. Merging two series stranded the alternate copies

**What it was.** There are two reasonable ways to ask what books are in a series, and the
app only had one of them. The one it had is the *listing* view: the one that fills the
page you look at. It leaves out books in the trash, and it leaves out alternate copies of
a book you already own, because showing you the same audiobook three times is not useful.

The merge used that listing view as its worklist. It moved everything the list contained,
then deleted the series. Anything the list had hidden simply stayed where it was, still
carrying the name of a series that no longer existed.

**Why it mattered.** Hiding a book from a page and leaving it out of a move are completely
different things, and the code treated them as the same thing. An alternate copy left
behind this way does not vanish and does not error — it just quietly loses its series, and
nothing in the app ever revisits it to notice. The Aug 14 report measured what this shape
of bug looks like once it has been running for a while: nearly seven thousand series
identifiers held by books on the shelves that no longer resolved to anything.

**The fix.** A second, complete way of asking the question was added alongside the listing
one, and the two paths that move books before deleting a series now use it.

## 2. A merge that silently under-applied a write

**What it was.** A third place used the same hidden-books list, but not to move anything.
After two authors are merged, one pass walks the books in the kept series and writes the
combined author credits onto each one.

**Why it mattered.** This one was not stranding anything — it was skipping work. Every
alternate copy was passed over, so it never received the merged credits, and unlike a
failure there is nothing to retry: no later pass re-checks those books. The omission was
permanent and completely silent. It is worth naming separately because it shows the blind
spot was never only about deletion; anywhere the app treats "the books in this series" as
a worklist, the hidden ones were being dropped.

**The fix.** Same change — that pass now reads the complete set.

## 3. What was deliberately left alone, and why

**What it was.** A fourth place asks the same question: the small preview card shown while
reviewing duplicate series, which lists a handful of the books involved.

**Why it mattered.** It would have been easy, and wrong, to "finish the job" and convert
this one too. It is display-only — it cannot move a book or delete anything, so it cannot
strand a row. Converting it would have re-introduced a *different* bug fixed earlier this
month, where a series listing showed every alternate rip next to the book it duplicates.
On a card capped at a few entries, the alternates would have crowded out the real books.

**The fix.** It stays on the listing view, with a note explaining that this is a decision
rather than an oversight, and the shared definition now states the rule plainly: display
may use the filtered question; anything that writes must use the complete one.

## 4. Two explanations this change itself made false

**What it was.** When the merge refused to delete a series, it told the operator that the
leftover books were "trashed or non-primary" — trashed, or an alternate copy. A code
comment said the same.

**Why it mattered.** Both were accurate before this change and both became half-wrong the
moment it landed, because alternate copies are now moved rather than left. A message that
names the wrong cause is worse than no message: it sends whoever reads it looking in a
place where there is nothing to find. This is the same failure mode that produced the
original bug — an explanation that outlived its reason and was believed rather than
re-checked.

**The fix.** Both were rewritten to name what actually remains: books in the trash, which
genuinely cannot be moved, and books whose move failed for some other reason.

## 5. A limit worth stating plainly

Two things this does **not** do, recorded so nobody later reads more into it than is
there.

First, "complete set" does not mean unfiltered. Books in the trash are still excluded from
both questions, deliberately — a book in the trash cannot be moved into another series.
The protection against deleting a series a trashed book still points at comes from the
separate guard built on Aug 14, which counts every reference regardless of state.

Second, that guard does not cover both merge paths. The scheduled overnight tidy-up
consults it and refuses to delete when something would be stranded. The merge you trigger
by hand does not, and deletes unconditionally. That gap predates this work and is not
introduced by it, but the honest reading is that this closes most of the hazard rather
than all of it.

## 6. Why the test is written the way it is

The task that commissioned this work came with a list of three places to change. There
were four. A checklist of places cannot catch a place nobody wrote down, and it certainly
cannot catch one added next month.

So the regression test is keyed on the two questions themselves: it asserts that the
complete answer always contains everything the listing answer contains, and differs by
exactly the hidden alternate copy — checked against both the in-memory and the on-disk
copies of the library, because those are two separate implementations that have disagreed
before. A new place that asks the wrong question still has to get past that property.

---

**Outcome.** Verified against both storage backends, with the fix deliberately broken
three different ways beforehand to confirm the new tests actually fail when the bug is
present rather than passing for unrelated reasons.
