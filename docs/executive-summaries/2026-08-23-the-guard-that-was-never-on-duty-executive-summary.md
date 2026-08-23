<!-- file: docs/executive-summaries/2026-08-23-the-guard-that-was-never-on-duty-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: ff4d9bd8-cca9-4ccc-9662-40ed62d0aa52 -->
<!-- last-edited: 2026-08-23 -->

# The guard that was never on duty

**2026-08-23 — a safety check was built after 13,322 books lost their series, and
then installed on a door the library never walks through**

## The short version

Back on the 14th we found that 13,322 books were pointing at series that no longer
existed. Something had deleted those series while books were still attached to them,
and a series name lives only in the row that was deleted — so once it was gone, it was
gone.

A safety check was built to stop that happening again. Before deleting a series, the
system now asks: *is anything still pointing at this?* If the answer is anything but
zero, it refuses.

**That check was real, and it never ran.**

There are two ways the library can answer that question: by reading the permanent
records on disk, or by reading a fast copy it keeps in memory. It always asks the fast
copy — that is the whole point of having one. The safety check had been added to the
disk-reading path. In the months it has been in place, every single real deletion asked
the other one.

The fast copy is now able to say "I know I am missing something", and when it does, the
question goes to the permanent records instead.

## 1. Why a fast copy is allowed to be incomplete

When the service starts, it builds an in-memory index of the whole library so that
ordinary questions get answered quickly. Building it means reading tens of thousands of
records, and occasionally one cannot be read — a value that will not decode, a row that
violates an internal rule.

The deliberate choice, and still the right one, is that **one bad record must not stop
the service from starting**. So the build skips what it cannot read and carries on.

The problem was not the skipping. The problem was that the copy then presented itself as
complete. A skipped record is a book whose series link nobody can see any more. Ask "how
many books point at this series" and the answer comes back **zero** — not "I could not
read one", just zero. Zero means "safe to delete".

So the exact disaster the check was built to prevent could still happen, and it would
arrive through the one door the check was not watching.

## 2. What we found when we went looking

Three things turned up that nobody had predicted, and one of them was in the proposed
fix itself.

**The suggested fix would not have worked.** The plan was to reuse a counter the startup
process already kept of records it had skipped. That counter only counted *one* of the
two ways a record can go missing. The other way — a value that will not decode — was
never counted at all. The demonstration written to prove the bug exists produces exactly
that uncounted kind of failure, so the proposed fix would have watched the wrong number
while the bug walked past it.

**Startup was not the only way to lose a record, and it was not the important one.** A
change can be saved to permanent storage successfully and then be rejected by the
in-memory copy a moment later. Same gap, no restart involved — and unlike a startup
problem, this one happens during ordinary running, which is where the service spends
essentially all of its life. It was being written to the log and otherwise ignored.

**Making losses visible immediately exposed an older bug.** As soon as the system started
reporting what it had lost, a completely healthy library began reporting a loss on every
single restart. One of the startup steps was missing a filter that every comparable step
beside it already had. That had been harmless while losses were invisible; it would have
become a permanent false alarm the moment anyone relied on the number. A false alarm that
fires every time is quickly ignored, which would have quietly disabled the very warning
we had just built.

## 3. Refusing is not good enough

The straightforward response to "the fast copy might be incomplete" is to refuse to
answer. That is safe, but it is not *useful*: the nightly cleanup job would simply stop
working until someone restarted the service, and a maintenance job that silently does
nothing is its own kind of failure.

Instead, when the fast copy knows it is short, the question falls through to the
permanent records. Those are authoritative, and their own reading path was repaired
first — it had three separate faults of its own, including one where it quietly ignored
any book whose internal identifier did not begin with an ordinary keyboard character.
Falling back to a broken fallback would have swapped one wrong answer for another.

The result is a **correct** answer rather than merely a safe one, and the cleanup keeps
running.

## 4. The same hole on the authors side

Authors have the identical check, with the identical problem, and one extra wrinkle:
a book's second and subsequent authors are recorded in one place only. Lose that record
and the co-author is referenced by nothing as far as the system can tell.

That matters because of scale. The bulk cleanup job that removes authors with no books
had identified **4,975 of 12,854 authors** as candidates for removal. An author's name,
like a series name, exists only in the row being deleted.

Two things were fixed there. The check now refuses when *either* of the two record types
it reads is known to be short — an earlier version copied the series version, which only
watches one, and that would have left the co-author case wide open. And a live feature —
the one that splits a wrongly-combined author into separate people — was creating records
the in-memory copy structurally could not accept, silently, on every use.

## 5. What we cannot tell you

**We do not know whether this ever actually deleted anything**, and that number is not
recoverable. A deletion that happened because a record could not be read leaves behind no
trace of the record it could not read. That is the nature of the failure.

What can be checked from here on is the count reported in the startup log. It is accurate
for the first time — previously it could read zero through a startup that had dropped
every unreadable record in the library — and any value above zero now means the slower,
authoritative path is being used instead.

## 6. Why this kept happening

The through-line in all of it: **a check that reports "nothing found" is indistinguishable
from a check that failed**, unless it is built to tell you which one happened. "Nothing
points at this series" and "I could not read the thing that points at this series" arrive
as the same number — and that number is the permission to delete.

Every fix here is the same fix in a different place: make the difference visible, and
make the permissive answer the one that has to be earned.
