<!-- file: docs/executive-summaries/2026-09-09-the-assistant-nobody-could-reach-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1d5c8e30-64a7-4f92-b0e3-9a72c4f61b85 -->
<!-- last-edited: 2026-09-09 -->

# The assistant nobody could reach

**Pull request:** [#3149](https://github.com/falkcorp/audiobook-organizer/pull/3149)
**Merge commit:** `57ab94321a1128f52bcc1854ce240d6c0a71c21b`

**Companion:** [The failures that reported success](2026-09-08-the-failures-that-reported-success-executive-summary.md)
([#3147](https://github.com/falkcorp/audiobook-organizer/pull/3147)) fixed how these
jobs *reported* themselves. This one is why they had nothing to report.

## Executive Summary

- When the app cannot work out a book's title and author from its file, it asks an
  AI to read the filename and make sense of it. Over the two days to 9 September,
  **81 of those jobs ran and not one of them worked out a single book.**
- The address the app had been given for its AI assistant was wrong by a single
  digit. Nothing was listening there, and nothing ever would be.
- That alone should have been a minor annoyance, because the feature was built to
  cope with exactly this: if the nearby assistant cannot be reached, hand the work
  to a second one. **The handover never happened, and the reason is the interesting
  part.**
- Each attempt to reach a machine that is not there was being retried — and then
  retried again by a second, separate retry mechanism inside the library that makes
  the call. The two did not add up, they multiplied: **nine attempts at an address
  that could never answer**, with pauses between them, which consumed the entire
  time allowance for the whole job.
- By the time the app was ready to say "fine, try the other assistant," there was no
  time left, so it skipped straight past it. The safety net was in place the whole
  time and was never once allowed to catch anything.
- **Every one of those runs took almost exactly 35 seconds**, whether it had one book
  to look at or a hundred and eighty-two. That is the signature of something timing
  out rather than something working hard, and it was visible in the run history the
  whole time.
- The app already knew. A status page had been reporting that the nearby assistant
  was unreachable, and saying precisely why, for as long as it had been broken.
  Nothing surfaced it and nobody was looking at it.
- **What changed:** the app now recognises "there is nothing at this address" as a
  different kind of problem from "the assistant is busy, try again." It stops
  immediately instead of retrying, which leaves the time budget intact and lets the
  handover to the second assistant actually happen.

**Still outstanding:** the wrong address is stored in production settings and has
not been corrected — that is a configuration change, not a code change, and it
needs to be made by hand. Until then the app will fall back to the second assistant
promptly and correctly, rather than failing outright, but the nearby one stays
unused. After correcting it, the AI backend status page should report the local
backend as reachable.

## 1. A retry inside a retry

**What it was.** Reaching out to an AI service can fail for boring, temporary
reasons — the service is busy, the network hiccuped — so the app retries a few
times before giving up. Reasonable on its own. But the software library that
actually places the call has its own retry logic built in, and the app could not
see it. Every single "attempt" the app made was quietly three attempts underneath.

**Why it mattered.** For a temporary failure this is merely wasteful. For a
permanent one it is destructive, because the whole feature depends on giving up
fast enough to try something else. The job had a fixed time allowance to work
within, and would only move on to the backup assistant if enough of that allowance
remained. Nine doomed attempts and the waiting between them used up essentially all
of it. The fallback was skipped not because anyone decided to skip it, but because
the clock had run out — and the code that skipped it recorded that as an ordinary,
unremarkable decision.

**What changed.** Failures that mean "there is no machine at this address" are now
told apart from failures that mean "the machine is having a bad moment." The first
kind stops immediately and hands over. The second kind keeps its retries, because
those genuinely do succeed on a second try.

Worth recording: it would have been easy to mark these failures as fatal instead,
which would have looked like a fix and made things worse — marking them fatal
abandons the entire job, which is the exact opposite of moving on to the backup.
The distinction between "this particular attempt is hopeless" and "this whole job is
hopeless" is the fix.

## 2. A comment that was confidently wrong

**What it was.** The code that manages the handover carried a written explanation
saying that an unreachable service costs almost nothing — that it fails in
milliseconds and the fallback proceeds unaffected. That explanation was wrong, and
it was wrong in a way that discouraged anyone from checking, because it named the
exact concern a reader would have had and dismissed it.

**Why it mattered.** The measurement contradicting it was not subtle: a
consistent 35 seconds per run against a supposed 30-second allowance. But a reader
arriving with a suspicion would find that suspicion already addressed in writing,
by someone who sounded certain.

**What changed.** The explanation has been replaced with what actually happens, the
arithmetic that produces it, and a note on why the tempting one-line fix is the
wrong one. A remaining limitation — the retry loop inside the third-party library
still cannot be switched off per-call, only entirely — is written down as a decision
to be made rather than left as a surprise for whoever looks next.

## 3. The diagnosis nobody read

**What it was.** Throughout the outage, the app's own AI status endpoint reported
the local backend as unreachable and gave the specific reason. It was correct,
current, and unread.

**Why it mattered.** The information needed to fix this in minutes existed for days.
It was not missing, it was merely somewhere nobody had cause to look — and nothing
about a page of jobs marked "completed" gave anyone that cause. This is the same
lesson as the companion report from 8 September, arriving from the other direction:
there, the failures reported themselves as successes; here, the failure reported
itself accurately, to no one.

**What changed.** Nothing yet, and this is named deliberately rather than quietly
closed. Surfacing backend reachability where the jobs themselves are visible is not
covered by this fix.

---

**Verification.** The new behaviour is covered by tests that assert an unreachable
backend is attempted exactly once and returns in well under a second, that ordinary
temporary failures still get their full three attempts, and that an unreachable
backend does not abort the surrounding job. The test that reproduces the production
failure rebuilds the error exactly as it arrives in production, wrapping and all,
rather than a simplified stand-in — an error that is classified correctly in a test
and unrecognisably wrapped in production is not fixed.
