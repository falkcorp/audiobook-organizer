<!-- file: docs/executive-summaries/2026-08-06-a-review-queue-you-could-not-work-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1f4e73b8-6d92-4c05-a71e-3820b5cd9f64 -->
<!-- last-edited: 2026-08-06 -->

# A review queue you could not work

> **Status.** Everything described here is merged and deployed, except the two
> items called out explicitly at the end as not yet done.

## The thing that was wrong

The library holds back anything it is not sure about. A folder full of audio files
might be one book split into chapters, or six different books that happen to sit next
to each other. Guessing wrong one way leaves a book in pieces. Guessing wrong the
other way **merges six novels into one and deletes five of them**. So uncertain cases
go to a review queue for a person to decide.

The queue had 777 items. **762 of them said the same sentence:**

> review: flat folder shares a title but ordering is unclear

That is not a queue, it is a pile. Every row looks identical, none says what it
actually found, and the only way to decide one is to go look at the files yourself —
at which point the queue has saved you nothing.

## Why it said the same thing every time

The one fact that separates the two cases was missing.

The difference between "six chapters" and "six books" is **how long each piece is**.
Six three-minute files are chapters; six two-hour files are books. Nothing in the name
reliably tells you — numbered sequels share a title just as chapters do.

The library knew this. The check existed and was correct. But it read each piece's
length from a record that, for **97.5% of the queue**, said zero — not "zero seconds
long" but "nobody ever wrote this down". A check that needs a number, given no number,
cannot fire. So it never fired, and the classifier fell back to the only thing left:
the folder name. Hence one sentence, 762 times.

The same missing number is what let a run earlier this month propose merging **41 of
43** groups that were actually separate novels.

## What was measured

Repair work in the preceding days had finally filled in those lengths for 16,000
books. Nobody had checked what that unlocked, and the queue's own item count barely
moved — which read like nothing had changed.

It had. Measuring all 356 remaining queue items against their now-known lengths,
**1,593 of 1,831 pieces have a real runtime.** The evidence had arrived; the queue
simply was not using it. Under the library's own existing rule, 286 of the 356 items
fall into a clear answer and 70 honestly cannot be called.

That measurement is what the remaining work is built on, and it corrected the
assumption everyone was operating under — that the queue was still starved of
evidence.

## What shipped

**Three items were one bad click from destroying books.** They were labelled
"multi-disc set", which normally means safe-to-combine. Their contents are two copies
of one whole book each — *Brother Wulf* at 6.3 hours twice, *Sevenfold Sword* at about
21 hours twice, *The Warring Son* at 11.8 hours twice. Combining those would merge them
and permanently delete one copy of each. A complete record of all 132 groups and all
4,146 files was written to disk first, because that operation cannot be undone and
there would otherwise be no record of what was lost.

**A repair job that could not finish now can.** A cleanup task was being killed partway
through. The cause was not the work — it was repeating an expensive bookkeeping step
once per row deleted instead of once per book, about 1.35 seconds of pure overhead each
time, measured. Batching it removes that repetition; the job should now complete in one
pass rather than needing a second run. (The end-to-end timing has not been re-measured
against the real library yet.)

**A second measurement pass was built** for the 1,000-odd folders that were never
checked properly the first time — they were sent to review not because they were
unknowable but because nothing had measured them. It has not yet been run against the
real library.

**Five security advisories in third-party code were closed.** One fix required a major
upgrade of the page-navigation library. That upgrade closes three problems and
introduces one that only applies to a mode this application does not use. The reasoning
is written down so nobody has to reconstruct it from the alert later.

**The queue now explains itself.** Each item carries a proposed action, the reason in
actual numbers — *"7 of 9 pieces run 90 minutes or longer, longest 15.8 hours — each is
book-length, so these are separate books, not parts of one"* — and the evidence behind
it. **286 of 356 items** get a decisive answer; the remaining 70 honestly say they
cannot tell, which is a useful thing for a queue to say and something it could not say
before. A person can overrule any of it, and the system then does what the person chose.

**A second measurement pass ran against the real library.** The ~1,019 folders that
were never properly checked the first time have now been measured: **434 of them can be
linked up confidently**, 585 genuinely do need a human. They had been sitting in review
not because they were unknowable, but because the check that would have settled them was
being called with the one piece of information missing.

## One fault worth understanding, because it would have hidden

Partway through, it emerged that **a person's overrule was not being written down
anywhere.** Only the fact of approval was stored. A later automated pass would re-read
the *machine's* original suggestion and act on that instead.

So someone could look at two books, say "these are different, keep them apart", watch
the system accept it — and have the system merge them and delete one, some time later,
with nothing linking the deletion back to the decision that was supposed to prevent it.
It would not have looked like a bug. It would have looked like the person's click simply
never happened.

That is fixed. The decision itself is now recorded alongside the approval, in a single
write so a crash cannot leave one without the other, and what gets replayed is what the
person chose. Two tests pin it from both directions — overruling *away* from a merge
must not merge, and overruling *toward* one must merge exactly once — and both fail if
the fix is removed.

## Still not done

Nothing in the library has actually been merged or separated. The switch that lets
approvals change real data remains **off**, deliberately, until a small batch has been
checked by ear against the snapshot described above. And the 434 linkable folders have
been identified but not yet linked — that is a write, and it is waiting on a decision.
