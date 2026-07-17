<!-- file: docs/executive-summaries/2026-07-17-dismissed-duplicates-came-back-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b2f7c1d4-96e3-4a58-8c02-1d7e4b93af65 -->
<!-- last-edited: 2026-07-17 -->

# Duplicates you dismissed kept coming back

## What was wrong

When the app thinks two books might be the same, it puts the pair in a review
queue. You look at it and decide: yes, merge them, or no, these are different —
dismiss it.

Dismissing didn't stick. The next time the duplicate scan ran, pairs you had
already rejected quietly returned to the queue as if you had never looked at
them. Your decision was thrown away, and there was no message saying so.

The effect compounds. Every scan handed back a batch of already-rejected pairs,
so the queue never got smaller no matter how much reviewing was done. Work spent
on the queue was being erased.

## Why it happened

Each scan re-records every pair it finds, and marks the ones it records as
"needs review". That's correct for pairs it's seeing for the first time.

The problem was that the code re-applied "needs review" to pairs that already
had a decision on them. It had been written carefully to protect other
information on the pair — how similar the books are, how that was calculated —
but the *decision itself* was left unprotected and got overwritten by the
incoming "needs review".

The rest of the system already assumed decisions were permanent. The cleanup
routine that removes old pairs explicitly refuses to touch dismissed or merged
ones. So this wasn't a deliberate choice — one path protected your decisions and
another quietly undid them.

## What we changed

A decision is now treated as a decision. Once a pair is dismissed or merged, a
scan cannot revert it to "needs review". Only another decision can change it —
for example, if you later decide a dismissed pair really should be merged.

Everything the scan is *supposed* to keep updating still updates freely, so this
doesn't make the system stubborn. It only stops it from forgetting.

## How we found it

We built a disposable copy of the live system — the real library, the real
database — isolated so nothing we did could reach production. Then we ran the
real duplicate scan on the copy and compared it against untouched production.

The comparison was unambiguous: **43 pairs** changed from dismissed back to
needs-review, and nothing else changed at all. Production, as a control, stayed
where it was. That is the kind of result that is very hard to argue with, and it
is not something we could have seen by reading the code alone — it took running
the real thing on real data.

## What this means going forward

Reviewing the duplicate queue is now worth doing: the queue can actually shrink
and stay shrunk.

One caveat: this fixes the behaviour from here on. Dismissals that were already
lost to previous scans are gone — those pairs are sitting in the queue again and
will need dismissing one final time.
