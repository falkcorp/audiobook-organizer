<!-- file: docs/executive-summaries/2026-08-31-august-monthly-roundup-executive-summary.md -->
<!-- version: 1.19.0 -->
<!-- guid: e7a3f109-52d8-4c6b-91f4-08b7c2d64e35 -->
<!-- last-edited: 2026-08-25 -->

# Executive Summary: August 2026 Monthly Roundup

**Period covered:** 2026-08-01 through 2026-08-25 (**month in progress** — this is
updated as work lands, not a closed record).
**Individual write-ups this consolidates:** the 29 dated summaries in this directory
from 2026-08-04 to 2026-08-19, linked inline below.

This is a monthly roundup rather than a single-change summary: each theme groups an arc
of related work instead of listing changes one by one. Everything below is written for a
reader who does not work on the code.

---

## The month in one paragraph

August so far has been about **things that lied**. Not features that were missing —
numbers, controls and safety checks that were present, looked fine, and were reporting
something untrue. A running time that said 158 hours for a twelve-hour book. A library
page that told a person with forty-four thousand books that they owned none. A review
queue with 777 items where 762 said the same sentence. A test suite reporting green
while half of it was broken. The work has largely been finding out which readings could
be trusted, and fixing the ones that could not.

---

## August 14: a thirty-fix day, and the search index finally tells the truth

One day produced thirty merged fixes and a fully verified redeploy. The thread
connecting most of them: **finishing off measurements that had been quietly
wrong.**

The biggest visible one: for weeks, the search index contained 3,983 entries
for books that had been deleted — searches could surface books that no longer
existed. A repair shipped earlier in the week ran at startup today and removed
every one of them, and the index now matches the library exactly, verified on
two consecutive restarts.

A safety check on a repair tool paid for itself twice. Before running a
planned iTunes identifier cleanup, a fresh count showed the recorded backlog
of ~9,000 items was long stale — the real number was two, both needing a
human decision rather than automation. And the check itself uncovered that
the tool's "preview only" switch silently did nothing when sent the way most
tools accept it — the request went straight to "apply." Nothing was damaged
(there was nothing left to apply), and the switch now works in both forms.

Monitoring is back. The metrics system had been unable to read the
application's health feed since that feed was put behind a password earlier
this month. The missing piece turned out to be one credential file; it's in
place, and the health dashboard has been live again since mid-afternoon.

Two library-wide repairs were measured before being run, and both
measurements changed the plan. A migration that moves book signatures out of
hot storage was previewed at 26,159 books — squarely in the predicted range,
so it proceeds next session with a small first batch. A repair that rewrites
correct metadata into the audio files themselves (fixing books whose files
still carry pre-correction tags) was trialed on 100 books and found to be far
too slow to run across the whole library overnight as hoped — so it was
deferred for a speed fix rather than left to run for weeks. The trial also
caught a real specimen: a file from one book still labeled inside as a
different book entirely.

## 1. Numbers that were wrong

**[Almost every audiobook was showing the wrong running time](2026-08-04-audiobook-running-times-executive-summary.md)** (Aug 4)

Book lengths were nonsense — a twelve-hour book listed as **158 hours**, another at
**294 hours** (twelve solid days), and in the other direction a seventy-eight-minute book
listed as **4 minutes**. This was not cosmetic: running time is the number other things
divide by, so wrong lengths quietly corrupted anything derived from them.

**[Series that were really book numbers](2026-08-06-series-that-were-really-book-numbers-executive-summary.md)** (Aug 6)

A series name is supposed to name the series. Many named the series *and* the book's
position in it, jammed into one string — `Evil Genius: Book 4: Becoming the Apex
Supervillain`. Every such book was its own one-book "series", so series that should have
grouped a dozen books grouped one.

**[The outage that marked the library broken — undone](2026-08-07-the-outage-that-marked-the-library-broken-executive-summary.md)** (Aug 7)

On July 1st the machine that listens to audiobook openings was unreachable for about a
day. Nothing was lost — but the system recorded the outage as if **every book it tried
that day had personally failed**. Roughly **31,000 books, about three quarters of the
library**, were marked "transcription failed" while being perfectly fine. This is the
clearest example of the month's theme: an infrastructure problem written down as a
property of the data.

---

## 2. Controls that did not do what they said

**[A review queue you could not work](2026-08-06-a-review-queue-you-could-not-work-executive-summary.md)** (Aug 6)

The library holds back anything it is unsure about, because guessing wrong can **merge
six novels into one and delete five of them**. That queue had 777 items and **762 of them
carried the identical sentence** — "flat folder shares a title but ordering is unclear."
A queue where 98% of entries say the same thing is not a queue, it is a wall.

**[Two controls that lied](2026-08-08-two-controls-that-lied-executive-summary.md)** (Aug 8)

After any server restart the library page announced **"No Audiobooks Found"** — not
"loading", not "cannot reach the server", but a plain statement that you owned nothing.
Two decisions made it that bad: a failed request **threw away the books already on
screen**, and the page could not distinguish "the request failed" from "there is
genuinely nothing here." Those two arrive looking identical and only one has an honest
answer.

---

## 3. The safety net, and the fact that it had stopped catching

This is the longest thread of the month, and it reversed itself twice. That is worth
reading as a sequence rather than a conclusion.

**[The safety net that had stopped catching — restored](2026-08-08-the-safety-net-that-had-stopped-catching-executive-summary.md)** (Aug 8) reported that the automated browser tests
were working again.

**[The safety net that was still half on the floor](2026-08-09-the-half-red-safety-net-executive-summary.md)** (Aug 9) established that this was **wrong**. The suite had
been checked with a command that hid most of its own output, against a server left
running for hours. Measured properly: **146 of 288 tests failing.** Roughly half the net
was still on the floor.

All 146 were then fixed — but the genuinely valuable output was **eleven product
regressions** the repair uncovered, including that **the library cannot be sorted from
the UI at all**, that **typing in the search box silently discards every active filter**,
and that the search box **is not debounced**, sending one full query per keystroke.

**And then a third correction.** Even at zero failures locally, the suite was still
failing on the build server — **179 failures there while showing zero on a developer
machine**. The causes were all environmental rather than product defects: image files
stored in a way the build server could not read, and two browsers competing for two
processor cores so aggressively that ordinary animations timed out.

**The outcome that matters:** as of Aug 9 the tests **block on every run** — a change
that breaks the browser suite can no longer merge. Both browsers now pass completely:
**544 tests, none failing.** That was the entire point, and until this month it was not
true.

Worth recording what the build-server failures turned out to be, because none of them
were faults in the product and two were written up as such before the evidence came in:
two browsers competing for two processor cores so hard that ordinary animations timed
out; a time limit that was fine for one browser and too tight for the slower one; and
image files stored in a way that meant the build server received a text placeholder
instead of a picture. That last one had made a check impossible to pass on the build
server since it was written.

---

## 4. Planning work: how search should be built

Not a change to the product, but a decision document written this month at the owner's
request, covering the options for search and their trade-offs.

Its central finding is worth stating: **a full search engine already exists in the
product and is running in production.** The problems people notice are almost entirely
elsewhere — the search box discarding filters, the missing debounce, and one ordering bug
where results are filtered *after* being cut into pages, which produces short pages and
wrong totals.

Two decisions were taken during the month: **sorting moves to the server**, and "the
system should not suck" is the bar. A follow-up section analyses what that costs, and
records an open question that has to be settled first — **the code disagrees with itself
about how many books the library holds**, and the answer changes the design.

---

## 5. Two search fixes that shipped

Both were on the "this has to stop" list, and both turned out to be a mechanism that
already existed being bypassed rather than a feature that was missing. Worth noting,
because "it's not there" and "it's there but skipped" look identical from the outside and
need completely different fixes.

**Searching no longer throws away your filters.** Filter the library to Organized, search
for an author, and you got matches from every state — while the Filters button still
showed a count, so nothing looked wrong. The page was switching to a *different* request
when you typed, and that request left the filters out. The server had always been able to
search and filter together; it simply was not being asked to. Search now goes through the
same request as everything else, which also means a filter added in future cannot be
wired into one path and forgotten in the other.

**The search box no longer asks the server a question per letter.** Typing a
ten-character title fired ten full searches of the library. A delay was already in place
and working — but the code that runs the search ignored it the instant the text was
parsed, which is immediately. So the brake existed and was never connected. Both values
now move on the same timer.

These compound: before this, typing ten letters sent ten searches that each ignored your
filters. That matters for the planned work to move filtering onto the server — no
server-side improvement counts for much while the browser behaves like that, and any
before/after measurement taken earlier was measuring the wrong thing.

Two tests that had been marked "expected to fail" are now ordinary passing tests.

---

## 6. Collections: a button the app had, and a feature the server did not

**What it was.** The mobile app has had a "collections" feature — a way to group books
into a named shelf — for as long as it has been connected to this server. The server
never implemented it. Pressing the button produced an error the app did not explain;
on 16 August it was tried five times in two seconds and failed every time. Worse, the
screen that *lists* your collections did have a working address, and it answered
politely with an empty list. There was no way to tell "the server has no collections
feature" from "you have not made any collections yet" — so the honest failure was
hidden behind a reassuring one.

**Why it mattered.** This is the second time this month the same shape caused a
problem: a feature that reports emptiness rather than absence looks like it is working
and leaves the user to conclude they did something wrong. Anyone who tried to organise
their library this way lost the attempt silently and had no reason to report a bug.

**The fix.** Collections are now built, in two forms. A **hand-picked** collection is
a shelf you add books to yourself. A **rule-based** collection is defined by a saved
search — "everything by this author added since January" — and fills itself from that
rule, so it stays useful without maintenance. Collections are shared across the whole
server rather than being private to one account, which is what was asked for: a shelf
one person curates is visible to everyone. Creating or editing one requires either
administrator rights or a new "manage collections" permission that can be granted on
its own; viewing them is open to anyone who can see the library, so handing someone
the ability to organise the shelves does not require handing them the keys to
everything else.

One limit worth stating plainly, because the word "dynamic" promises more than the
first version delivers: a rule-based collection re-runs its rule when someone opens it
in the web interface, or when a refresh is explicitly requested. Nothing yet refreshes
it in the background. A collection created and then only ever viewed in the mobile app
will show the membership it had when it was created. That is a deliberate first
version, not an oversight, and the follow-up is tracked.

Two defects were caught in this work before it shipped rather than after, both by
tests written specifically to disprove the change rather than confirm it. The first:
reserving the collections address space for the mobile app was done too broadly, and
would have broken the very web-interface features it was meant to support — the same
mistake, in the same file, that took out working features twice earlier this month.
The comment justifying it was accurate when written and false one commit later,
because the second commit created the thing the first had verified did not exist. The
second: simply *viewing* a rule-based collection wrote it back to disk every time,
which would have made every collection look freshly modified on every glance and
defeated the caching the app relies on to stay fast.

---

## 7. August 17: a check that could not fail, and the cleanup jobs

Three changes that are invisible from the app but change what the code can quietly get
wrong.

**A safety check was deleted rather than repaired, because it was proven unable to fail.**
One of the automated checks claimed to catch a specific kind of mistake: a piece of
scaffolding drifting out of step with the real thing it stands in for. It was tested by
deliberately making exactly that mistake — and it reported success. The reason is
mundane: it was rebuilding the scaffolding with a command that, in this project, rebuilds
nothing, then checking whether anything had changed. Nothing ever had. It was removed
rather than fixed, because three other checks *do* catch that mistake, and all three were
confirmed to go red on the same deliberate break. **A check nobody has ever seen fail is
not evidence of health.**

**The 37 maintenance jobs each got their own registration.** These are the one-off cleanup
tasks — repairing file links, recomputing totals, tidying up duplicates. All 37 shared a
single registration, which meant they also shared one answer to questions like "if the
server restarts mid-run, what happens?" and "how long may this take?". Giving each job its
own entry made per-job answers possible for the first time, and **four jobs turned out to
need a different answer than the shared one gave them.** The other 33 were confirmed
unchanged rather than assumed so.

**And those jobs' reach into the database was cut roughly in half.** Every cleanup job was
handed a key to all **398** database operations, whether it needed them or not. Measuring
what they actually use gave **187** — so that is what they get now. This is a guardrail,
not a fix: nothing was doing the wrong thing, but a job that starts reaching somewhere new
now has to say so where a reviewer will see it.

Worth recording honestly: an earlier framing of this work claimed it would delete a large
piece of test scaffolding. **It does not,** and that was corrected before the work shipped
rather than discovered afterward.

---

## 8. August 18: breaking up the parts lists

**What it was.** Inside the program, whenever one piece of code needs to ask the
database for something, it first declares a list of what it intends to use — a
parts list. Done well, that list is short and specific: "I read books and I write
book files," four or five entries. Done badly, it says "give me everything," and
over time a great many of them had come to say exactly that. The worst offender
declared access to **182 different database operations** in a single line.

**Why it mattered.** Not because anything was broken today — nothing user-visible
was. It mattered because those lists are the only honest record of what a piece of
code can reach, and once one says "everything," it stops being a record at all. A
new change can quietly acquire the ability to modify anything in the library
without anyone deciding it should, and a reviewer looking at the file has no way to
tell what it actually touches. Several of this month's genuine bugs — metadata
being silently overwritten, files being repointed across the wrong books — are the
kind that hide comfortably behind a parts list nobody can read.

**The fix.** Nine changes, merged over one night, cut the number of oversized lists
from **28 to 5**. Most were split into small named groups, so the same code keeps
working unchanged while a reader can finally see what it uses. Three findings from
doing the work are worth recording:

- **One of the biggest was entirely dead.** The 182-entry list, and three others
  beside it, were referenced by nothing at all — left behind when the code they
  described moved elsewhere months ago. The automated checker had been faithfully
  reporting them as things to tidy up; the honest fix was to delete them.
- **The width spreads through function arguments, not declarations.** One group of
  seven small helper functions had each been written to accept the entire database
  rather than the single operation it used. That one habit was forcing width onto
  everything that called them; fixing the seven signatures was what made the
  largest remaining split possible at all.
- **Five were deliberately left alone, and that is written down.** They are not
  badly organised — they are genuinely large, and rearranging them would produce a
  tidier-looking declaration in front of the same sprawl. One of them fronts a
  component that uses **44 distinct database operations**; the finding there is
  that the component is too big, and no amount of tidying its parts list will
  change that.

Finally, a guard was added so this cannot quietly come back: an automatic check now
fails any future change that increases the count past 5, with a documented way to
override it deliberately when there is a real reason.

---

## 9. August 20: asking what the "certain" duplicates were certain about

The app keeps a queue of suspected duplicate books — pairs it thinks might be two copies
of the same thing. About nineteen thousand of those pairs were old enough that the app had
never written down **why** it suspected them. It had a verdict with no reasoning attached,
which meant two of its own tools refused to touch them: neither could re-check a verdict
whose evidence was missing.

A job to go back and fill in that missing reasoning already existed, and it had been run
in preview mode. The preview said it would fill in 18,311 pairs. What it did not say was
what the reasoning would *look like* — and one of the sample lines was worrying. It showed
a pair rated **"certain"** on the strength of a **single** piece of evidence, in a
population where the most common evidence by far is "the titles and authors look similar".
A similar title alone declaring two books identical is exactly how two genuinely different
books get merged into one.

So the job was taught to report not just how many pairs it would fill in, but **what kind
of evidence they rest on** — how many land in each confidence level, and, for the "certain"
ones, exactly which combination of evidence produced that rating.

The answer, measured against the real library:

- **Not one "certain" pair rests on a title match alone.** Every single one is backed by
  an identical audio file, a matching ISBN, or an identical metadata record. The worrying
  sample turned out to be an identical-file match — the strongest evidence the system has.
- It could not have gone the other way. A title match is capped at a confidence that
  cannot reach "certain" no matter what else supports it. The measurement confirmed the
  cap is actually in force in the live system, which could not be checked any other way.
- **The confidence levels are not evenly spread — the middle one is empty.** 1,469 pairs
  are "certain", 16,582 are "medium", and **zero** are "high". That is a property of the
  evidence itself: the weak evidence tops out just below "high" and the strong evidence
  starts just above it, so nothing lands in between.
- Of the 1,469 "certain" pairs, only **11** carry enough independent corroboration to be
  eligible for automatic merging. The other 1,458 still require a person, because the
  system asks for two *independent* strong signals, not one.

With that in hand, the fill-in was run for real: **18,311 pairs, no errors**, and verified
afterwards by a separate re-count rather than by trusting the job's own tally. No book was
merged, deleted, or changed — only the missing reasoning was written down.

One finding came out of this that is worth flagging on its own: the pairs filled in this
way have **no "reasons to doubt" recorded against them**, because the recalculation path
never computes them. One of the automatic-merge safety checks asks "does this pair have
any reasons to doubt it?" — and for these eighteen thousand pairs, that check can only
ever answer no. It is not currently reachable in a way that matters (automatic merging is
off by default and behind a separate switch), but a safety check that structurally cannot
fire is worth knowing about before anyone turns that switch on.

---

## 10. August 20: one setting, one way of reading it

**What it was.** A full inventory of the program's 565 configuration options,
finished earlier the same day, turned up something specific: 25 places across
the backend were reading a setting straight from the operating system's
environment at the moment they needed it, instead of through the one shared
system every other setting already goes through, which reads each setting
once when the program starts and remembers the answer. A setting read the
live way can disagree with itself mid-run depending on which piece of code
happens to ask first, and it also means the same knob can be spelled two
different ways in two different files with no way to tell from the outside.

**Why it mattered.** Nothing was on fire — this was a consistency problem, not
an outage. But it is exactly the kind of gap that produces one later: a
metadata-lookup web address overridden for a test in one file quietly having
no effect on a sibling file that still reads the environment its own way, or
a dry-run safety switch for the iTunes write-back feature that a test
believed it had turned on, while the code path it was testing never looked at
that switch at all. Both of those were real, found only because fixing the
live reads broke the tests that had been (accidentally) relying on the old
behavior.

**The fix.** All 25 call sites now read from the shared, once-at-startup
settings system, matching how every other option in the program already
works. Two of them could not simply add the usual import — one because doing
so would have created a circular dependency between two core packages, the
other because that file is deliberately kept independent so it can be moved
to a separate add-on system later — so each was given the same kind of
narrow, purpose-built accessor already used elsewhere in the codebase for
that exact situation, rather than bending the rule to fit. A stray override
address for the AI provider was also widened in scope: it had been named as
if it only mattered to an internal benchmarking tool, but it turned out to
also control the address the production AI features actually call. Fixing
the live-environment reads surfaced eleven tests that had been quietly
passing for the wrong reason — they set an environment variable and expected
an immediate effect that the new, correct code path no longer provides
without an explicit refresh — and all eleven were corrected to ask for that
refresh explicitly, the same way the shared settings system already expects.
The example file developers copy from when adding new settings was also
cleaned up: four entries that had never done anything (documented for years,
read by no code) were removed, and every setting introduced by this change
was documented in their place.

---

## 11. August 20: the safety check that could not fire

Section 9 ended by flagging something and setting it aside: one of the safety checks
guarding automatic merging could only ever answer "no reasons to doubt." This is what
that turned out to be, and what was done about it.

**The check.** Before the app will merge two books together on its own, it runs through a
short list of refusals. One of them asks: *does this pair have any recorded reasons to
doubt it?* There are three such reasons the app knows how to detect — the two books are
already filed as two formats of the same title, they are clearly different volumes of one
series, or they are two chapters of a single book sitting in the same folder. Any of them
means "these are not duplicates, do not merge."

**The problem.** That check read the reasons off a note attached to the pair's saved
verdict. Nothing in the app ever writes that note. The one function capable of filling it
in is called from three places, and all three pass in an empty list. Worse, the routine
scan never gets the chance: when it detects one of the three reasons, it **throws the pair
away entirely** rather than saving it with the reason attached. So a pair that was
correctly identified as "do not merge" leaves no record behind at all.

The result is a check that reads a field nothing fills in. It has never refused a single
pair, and as written it never could have. This was originally believed to affect only the
eighteen thousand pairs from section 9's fill-in job. It did not — it affected **every
pair in the library**.

**Why it had not been caught.** There was a test for it. The test passed. It created a
pair, wrote the reasons onto it by hand, and confirmed the merge was refused — a shape
real data never takes, because nothing writes those reasons by hand or otherwise. The test
was checking that the code does what it says when handed input it will never receive. It
passes just as happily with the broken code as with the fixed code; this was confirmed
directly, by removing the fix and watching it stay green.

**The fix.** Rather than trying to backfill a note onto eighteen thousand records, the
check now works the reasons out **on the spot**, from the two books themselves, at the
moment it is deciding whether to merge them. It already had both books in hand and the
calculation is pure arithmetic on data already loaded, so this costs nothing. It is also
more correct than the saved note would have been: if two books become "do not merge" after
their verdict was recorded — someone files them as two formats of one title next week —
the live check notices and the saved note never would.

The old note-reading check was kept in front of the new one rather than deleted. Whether
any very old record carries reasons written by a previous version of the program could not
be confirmed, and removing the check would have quietly changed how those records are
treated. Keeping both costs one line.

Two new tests cover it, built the way the old one should have been: the reasons are left
blank, exactly as real data has them, and the two books are made genuinely un-mergeable.
Both fail when the fix is removed.

**What this did and did not change.** No merge behaviour changed in production, because
automatic merging is switched off and always has been. What changed is that the switch is
now safe to consider turning on. Before this, doing so would have handed the merge
decision to a list of refusals with a hole in it — and the eleven pairs currently eligible
for automatic merging would have been assessed by a guard that had never once said no.

**Alongside it,** the project's code-quality gate was fixed. It had been failing on three
warnings for long enough that failure had become the normal result, which is the condition
under which a real warning goes unnoticed. Two were genuinely harmless leftovers. The
third was not: it reported an unused helper, but the helper was unused because a scoring
routine had been hand-copying what the helper does instead of calling it. Deleting the
helper would have silenced the warning and left the copied version with nothing to be
compared against — so the scoring routine was pointed at the helper instead. The
arithmetic is pinned by reference figures, and rather than assume those figures covered
the change, they were checked by deliberately breaking it: the reference figures caught it
both times.

---

## 12. August 20: two checks nobody was running

Section 11 described a safety check that could never say no. This is a variation on the
same idea, and arguably a starker one: two checks that were not broken at all. They worked
perfectly. Nothing ever asked them anything.

**How they came to light.** Neither was discovered by the automated checks that run on
every change — those were, and had been for months, entirely green. They surfaced by
accident, while someone was verifying an unrelated piece of work and happened to run the
full set of checks by hand.

**The first check.** The app can be extended by other programs, and the toolkit they plug
into is meant to be a stable, narrow public contract. There is a guard whose whole job is
to notice when internal parts of the app quietly leak into that toolkit. It had been
reporting a failure since 18 July — thirty-three days — and no one had seen it, because
the guard was only ever wired into a command a developer runs on their own machine. It
appeared nowhere in the automated checks. Searching the automated checks for its name
found matches only inside comments *about* it.

The three things it was complaining about turned out not to be real problems. All three
were pulled in indirectly, through parts of the app the guard had already approved, and
none of them are visible to anyone writing a plug-in. But that is exactly what made the
guard's design unworkable: it compared against a hand-maintained list of approved parts,
and every time an unrelated piece of work added something indirectly, the list fell
further behind. Five of its nine existing entries had already been added this way, one at
a time, each without explanation. It was losing a race it could not win.

So rather than adding three more names to a list that had already failed five times, the
guard was rebuilt around the distinction that actually matters. What a plug-in author can
*see* is now a short, deliberately-chosen list that a person must edit by hand, and which
no automatic step can quietly extend. Everything pulled in indirectly is now compared
against a recorded snapshot instead. When that set legitimately grows, accepting the
growth means updating a file that is kept alongside the code — so the change shows up in
review, where somebody looks at it, rather than as a line of text in a log nobody reads.

**The second check.** A body of benchmarking tooling — used for tuning how the app decides
two authors are the same person — is kept behind a switch, so that ordinary builds skip
it. On 18 April, a reorganisation moved two pieces of shared machinery to a new home and
updated everything that referred to them, except four places inside that switched-off
tooling. Because no build anywhere turned the switch on, nothing ever tried to compile
those four places. The tooling had not been buildable for **four months**, and not one
automated run said so.

The repair itself was four lines. The interesting part is that the project already
possessed a command that would have caught it on day one. It had simply never been run
anywhere either.

**What was actually fixed.** Both faults were repaired, but the durable change is that
both checks now run automatically on every proposed change, and both are also part of the
single command a developer runs before submitting work — so the two can no longer drift
apart, with one of them quietly checking less than the other.

**On trusting the new guard.** A guard that reports success is not evidence of anything
until it has been seen to fail. Before this was accepted, it was deliberately broken four
ways — a forbidden connection added by hand, an unrecorded addition, a stale entry, and a
clean run — and it gave the right answer each time. The same was done to the small tests
covering it: each was checked by deliberately damaging the code it protects and confirming
it noticed. Two of five initially appeared not to notice, which turned out to be a fault
in how the damage was being applied rather than in the tests; once corrected, all five
caught it.

**One caveat, stated plainly.** These two checks now report on every proposed change, but
they cannot yet *block* one. The list of checks that must pass before work can be merged
is a separate setting, and adding to it is a deliberate administrative decision rather
than something that follows automatically. Until that is done, a change that re-breaks
either of these will be flagged and can still be merged.

**And a third instance, found while fixing these two.** The project's code formatter is
also verified nowhere — not in the automated checks, not in the developer command — and
forty-three files across twenty-four areas of the code have drifted out of shape as a
result. That has been written down as follow-up work rather than folded in here, because
tidying forty-three files at the same time as repairing two safety checks would have made
both harder to review. It is noted because three instances of the same oversight in a
single day is no longer a coincidence: the project has no rule that a check must be
reachable by the automated system in order to count as a check.

---

## 13. Two days, one dead page (Aug 10–11)

**[The invisible sheet that made the page stop responding](2026-08-10-the-invisible-sheet-executive-summary.md)** (Aug 10)

Closing a dropdown or the filter panel could leave the whole page dead — every click
landing on a transparent, invisible sheet left behind by a closing animation that never
finished. The bug had been investigated and dismissed twice before: once blamed on a slow
test machine, once missed because the check only looked for the menu to become "hidden,"
which it already was. Invisible and harmless are not the same thing.

**[The fix that only moved the window](2026-08-11-the-fix-that-only-moved-the-window-executive-summary.md)** (Aug 11)

The previous day's fix had been reported as complete. It was not. Removing the closing
animation made the failure much rarer, not impossible — the software still scheduled a
"finish closing" step for later, just later now meaning immediately instead of a fifth of
a second, and the bug lived in that remaining gap. It is now demonstrably fixed rather
than merely believed to be.

---

## 14. Requests that quietly went to the wrong place (Aug 11–14)

**[The instructions that were thrown away](2026-08-11-instructions-that-were-thrown-away-executive-summary.md)** (Aug 11)

Telling the organiser to scan, organise, or convert a specific set of books could
silently run the job on **the entire library instead**, with no warning and success
reported regardless. The fault was in how a request got packed and unpacked on its way to
the code that does the work. It is fixed going forward — not confirmed as the *only*
possible cause on the live server, and existing records were not repaired retroactively.

**[The endpoints that answered anyway](2026-08-13-the-endpoints-that-answered-anyway-executive-summary.md)** (Aug 13)

Opening a series could show books that had nothing to do with it while the series itself
claimed zero books, and playlists opened empty every time. All three faults were requests
the organiser accepted and answered confidently, but wrongly — none were caught by the
existing conformance suite because none of its 28 recorded examples carried a search,
filter, or page instruction to begin with.

**[The preview button that was not a preview](2026-08-14-the-preview-button-that-was-not-a-preview-executive-summary.md)** (Aug 14)

18 of the 34 maintenance jobs advertise "preview only" as their default, and the part of
the server that actually starts a job had the opposite idea of the default — applying
changes for real whenever the preview setting wasn't spelled out explicitly, which is
exactly what happens when something trusts the published default. Fixed for all 18; a
separate duplicate-series routine still has no preview option at all, though it has never
been run against this library.

---

## 15. A day of checking our own claims (Aug 12)

**[Checking our own homework](2026-08-12-checking-our-own-homework-executive-summary.md)** (Aug 12)

A single day spent verifying claims — mostly the team's own — found a phone app told a
feature existed when it didn't, two drifted specifications with 48 described features
that don't actually exist, a maintenance script reporting success after doing nothing,
and phone-app compatibility tests that checked the *shape* of an answer but never what
was *in* it. Six of the day's own findings turned out to be wrong and were caught and
corrected the same day — nothing described here was in production yet as of this write-up.

---

## 16. Zero results is not a neutral answer (Aug 12–14)

**[The second page that was never there](2026-08-12-the-second-page-that-was-never-there-executive-summary.md)** (Aug 12)

Every search's second page came back empty, always, because the code cut results into
pages *before* applying your filters instead of after — and the "number of results" shown
was really just the count of rows that happened to fit on the current page. Reversed and
fixed; every earlier assessment of search quality had unknowingly been using this same
broken count.

**[The books search could not see](2026-08-13-the-books-search-could-not-see-executive-summary.md)** (Aug 13)

The library's separate search catalogue is built in one pass, oldest book first; if the
server restarts partway through, the pass stops where it is and never resumes — leaving
16,738 books with no searchable "card" at all. Repaired and deployed on 2026-08-13, with
the catalogue now finishing an interrupted pass automatically instead of stopping forever.

**[Deleted, but not gone](2026-08-13-deleted-but-not-gone-executive-summary.md)** (Aug 13)

Chasing eleven books that resisted an unrelated repair uncovered that **3,953 deleted
books** — trashed, meant to be recoverable — had never actually stopped being processed,
because the library's "give me all the books" function exists in two versions and only
one of them excludes the trash; production used the wrong one. Along the way, an earlier
report of a 14 TB "disk emergency" was corrected to zero actual reclaimable space, an
error caught and corrected the same session. The fix is merged but, as of this write-up,
**not yet run** against the live library. (This and [The Books Search Could Not
See](2026-08-13-the-books-search-could-not-see-executive-summary.md) explain the same
symptom — a search for *All Jobs and Classes* turning up nothing useful — two different
ways; both faults are real and independent.)

**[When quotes did not mean quotes](2026-08-13-when-quotes-did-not-mean-quotes-executive-summary.md)** (Aug 13)

Three separate defects made search return far too much at once: a trailing `*` wildcard
that only worked in lowercase, quotation marks stripped before the search ever saw them,
and common words like *all* and *the* deleted in a way that broke the position-tracking
quoted phrases depend on. All three are fixed; a matching capital-letter bug in fuzzy
search and over-aggressive word-splitting on fields meant to be exact (genre, tags, ISBN)
are known and filed separately.

**[Thirteen search boxes that always said no](2026-08-14-thirteen-search-boxes-that-always-said-no-executive-summary.md)** (Aug 14)

Thirteen ways to narrow your library — release year, ISBN, file size, and ten more —
silently returned "no books found" instead of admitting the server didn't recognise the
field, because the search box and the server kept separate lists of what could be
searched that nobody had ever cross-checked. A test now reads both lists and fails the
build the moment they disagree.

---

## 17. Records that said something that was not true (Aug 14–16)

**[The series that vanished from under 13,322 books](2026-08-14-the-series-that-vanished-from-under-13322-books-executive-summary.md)** (Aug 14)

A weekly cleanup job deleted series it believed had zero books, using a counter built for
a website badge that deliberately ignores trashed and duplicate copies — the wrong test
for "is anything still pointing at this series?" 13,322 books ended up filed under a
series that no longer existed. The count is fixed going forward; undoing the existing
damage is a separate, filed follow-up.

**[The work that said it succeeded](2026-08-16-the-work-that-said-it-succeeded-executive-summary.md)** (Aug 16)

Six unrelated places shared one mistake: a job failed and the program reported success
anyway — file renames marked "applied: true" when nothing moved, organise operations left
pointing at an empty folder, and three iTunes maintenance jobs that had done nothing since
mid-July because an empty placeholder version silently won out over the real one.
Reporting progress is now an enforced requirement rather than a convention; three
scheduled jobs — including the one that applies this month's file-location fix — were
separately found to be wired up but unable to ever run, and were left switched off
deliberately pending a decision to run them under supervision.

---

## 18. What the audio files carried, and where they ended up (Aug 13–15)

**[One chapter, twenty-four hours](2026-08-13-one-chapter-twenty-four-hours-executive-summary.md)** (Aug 13)

Almost no audiobooks in the library had working chapter navigation — a 24-hour book
opened showing a single chapter spanning the whole recording — because the organiser only
reads embedded chapter data the first time it sees a file, a behaviour added in July 2026
that permanently skipped every book already in the library at that point. Fixed for the
whole library; multi-file books were deliberately left alone, since their existing
one-chapter-per-file guess is already correct.

**[The ampersand that became an author](2026-08-14-the-ampersand-that-became-an-author-executive-summary.md)** (Aug 14)

46 people were filed twice in the author list — once correctly, once as "& Firstname
Lastname" — because multi-narrator credit lines were split at commas before an existing
ampersand rule ever got the chance to run. 145 books were affected out of roughly 9,350
authors; nothing was deleted, only duplicated.

**[The tag that was never written](2026-08-15-the-tag-that-was-never-written-executive-summary.md)** (Aug 15)

Multi-file audiobooks were being completely rewritten on every metadata save, whether or
not anything had changed, because the before/after comparison didn't know about the
track-number tag and the code that writes files didn't save that tag either — each gap
hid the other, so the same full rewrite ran forever for information that never once
reached disk. Fixed and verified against a real file; how much time this saves across the
whole library has not yet been measured.

**[The tug-of-war over where books live](2026-08-15-the-tug-of-war-over-where-books-live-executive-summary.md)** (Aug 15)

Books kept moving on their own because three different operations — organising, saving
metadata, and organising a multi-file book — each computed a book's correct folder
location a different way, and every individual move reported success while the library
never settled. Unified to one answer; three related problems surfaced during the fix and
were deliberately left for later, filed with their evidence rather than folded into an
already-wide change.

---

## 19. Underneath: performance and infrastructure (Aug 12–19)

**[The page nobody was looking at](2026-08-12-the-page-nobody-was-looking-at-executive-summary.md)** (Aug 12)

Opening the Activity page started a job that read the whole activity history into memory,
and closing the tab before it finished did not stop it — every reopen started another
copy, and 30 of them running at once brought the server down with an out-of-memory kill,
five separate times. Fixed and verified on 2026-08-12: the same page now answers in **55
milliseconds**.

**[The 580 megabytes read and thrown away](2026-08-13-the-580-megabytes-read-and-discarded-executive-summary.md)** (Aug 13)

Every server restart spent about two minutes loading the library, roughly 80% of which —
**580 megabytes** of book "signature" fingerprints — was read off disk and discarded
within microseconds, by design, because it was stored in the same record as everything
else the startup index needs. The code that stops reading it is merged and deployed; the
one-off move of the existing signatures out of that shared record had **not yet been run**
as of this write-up.

**[Untangling the wiring](2026-08-19-untangling-the-wiring-executive-summary.md)** (Aug 19)

Every part of the program used to reach the database through one shared connector with
**398 operations** on it, whether a component needed two or two hundred — making it
impossible to tell what a change might break, forcing tests to fake the entire database
(one stand-in ran to 24,613 lines), and letting things silently come unplugged. Narrowing
components down to what they actually use surfaced four features that had quietly stopped
working — including the activity log's fast storage being switched off and a library
warm-up step that sometimes skipped on startup — all now fixed. Splitting the underlying
database object itself into pieces, so the giant connector can be deleted outright, is the
work still ahead.

---

## 20. Files that were not where the database said (Aug 17–19)

**[The books that would not download](2026-08-17-the-books-that-would-not-download-executive-summary.md)** (Aug 17)

Measured directly, **41.8%** of a random sample's file entries pointed at files that were
not there — every one of them in the folder tree the organiser manages itself, none in the
iTunes tree, meaning most affected books still have a real, playable copy elsewhere. The
author and narrator pages that showed no books are fixed and deployed; repairing the dead
file entries is understood precisely but deliberately left as a decision for a person,
since removing an entry is unsafe for the books where every entry is dead.

**[The repair that would have deleted the evidence](2026-08-19-the-repair-that-would-have-deleted-the-evidence-executive-summary.md)** (Aug 19)

A cleanup job meant to remove dead file-path entries was still waiting on approval that
had never been given — which turned out to be what saved the underlying files. A
five-and-a-half-month naming bug (a slash in a track-number template, which the filesystem
reads as "make a folder") had left many of those entries pointing at a real file under a
slightly different name rather than at nothing. **The actual repair does not exist yet**
— this work only made the situation safe to measure, and the job now reports and waits for
a person rather than deleting anything.

---

## 21. "You have no credit left" read as "try again in a moment" (Aug 23)

The organiser can use a paid AI service to read details out of audiobook files. That
service is bought with credit, and when the credit runs out it says so plainly in its
reply.

The organiser did not recognise that message. The reply carries the reason in two
separate places, and the organiser only ever looked in one of them — the one the service
happens **not** to use for this particular problem. So "your balance is empty" was read as
"the service is busy right now", which is a completely different situation: busy means
wait a moment and try again, and that is exactly what it did. Three attempts per batch of
files, with a pause between each, against a service that had already given its final
answer.

**What it cost, once, on 16 August.** All 77 batches of a library scan failed this way,
one after another. Because every batch was quietly waiting and retrying, the scan made no
visible progress, and after five minutes the scan's own safety timer concluded it had
hung and cancelled it. That threw away a completed pass over **3,917 files** — a pass that
had worked perfectly. The only thing wrong was that the organiser could not hear the word
"no".

The same fault sat in a second, separate job: the one that builds the searchable index of
the library. Nobody had noticed, because that job fails more quietly. Against an empty
balance it made three times as many requests as it needed to, and waited an extra five
seconds, for every batch of an entire library.

**Why no test caught it.** There was a test for precisely this, and it had been passing
for months. It passed because it wrote its own copy of the service's reply — shaped the
way the code expected, rather than the way the service actually sends it. So the test was
not checking the organiser against reality; it was checking the organiser against the same
misunderstanding the organiser already had, and agreeing with it. A test like that can
never fail, and its passing is not evidence of anything.

What makes this one sting is that the project had already learned this lesson and written
it down. A note sitting in a neighbouring part of the code says, in almost these words,
that inventing the service's reply would let a check pass that could never work in real
life — and next to that note is a genuine recorded reply, kept for exactly this reason.
It was in a part of the code nobody working on this problem would have opened. **Writing a
warning down somewhere does not put it anywhere it will be read.**

Both are fixed. The organiser now checks both places the service might state the reason,
and the test has been rebuilt around the real recorded reply. The fix was then deliberately
sabotaged four different ways to confirm the new test actually notices — including
restoring the original bug, which it now catches.

## 22. A week of housekeeping that never happened (Aug 17–23)

Every night at 5am the organiser runs a maintenance window: a list of twelve
housekeeping jobs that tidy up after the day's work — refreshing the duplicate
caches, clearing out temporary files, emptying the trash, pruning old logs,
optimising the database.

For seven consecutive nights, **none of the twelve ran.** Not one.

An earlier draft of this entry said three nights, and flagged that three was a
floor rather than a count — the records had only been searched back that far.
Searching the full week settled it. Every maintenance window from the 17th to
the 23rd failed, at 05:00, with the identical fault: seven for seven, no
exceptions, no partial successes.

The boundary is exact, and it is the strongest evidence here. The change that
caused this went in at 8:34pm on the 16th. The maintenance window that had run
that same morning — hours *before* the change — did not hit this fault. The
very next one, at 5am on the 17th, did. So did every one after it. The first
opportunity the bug had to fire, it fired, and it never once missed
afterwards.

What makes this worth writing down is how it failed. The jobs themselves were
perfectly healthy. The duplicate-refresh job — the first on the list — started
on time and finished successfully on all seven nights, working through every
author in the library each time (12,856 of them on the first night, 14,948 by
the last, as the library grew underneath it). The problem was the supervisor: the part of the system whose only job is
to stand and watch each task finish so it can start the next one.

Five seconds into watching the very first task, the supervisor crashed. And
because the supervisor was the thing holding the list, everything after that
first job simply never started. The nightly report recorded the whole window as
"failed, 0 of 12," while the one job that *had* run sat elsewhere in the
records marked "completed."

### Why it crashed

Earlier in the summer the organiser moved its job-tracking to a new system.
The supervisor was never updated: it kept looking up each job in the *old*
filing cabinet, using a reference number issued by the *new* one. It was
always going to come back empty.

The specific trap is that "I looked and found nothing" and "I looked and
everything went fine" were reported the same way — as an absence of any error.
The supervisor checked whether something had gone wrong, was told no, and
proceeded as though it were holding a record it had never actually received.

### What was fixed

The supervisor now reads from the current system. Two other problems came out
with it:

- The obvious quick fix — "if there's no record, stop waiting" — would have
  been considerably worse than the crash. It would have made the supervisor
  stop waiting *instantly*, launching all twelve jobs simultaneously instead of
  one after another, **and reporting success while doing it.** A crash is at
  least visible. That was avoided deliberately.
- The supervisor did not recognise two of the ways a job can legitimately end
  — most importantly the ordinary "the server restarted while I was working"
  ending, which is how the library scan finishes on most nights. Even with the
  crash fixed, it would have waited forever on those.

Interrupted jobs are now also reported honestly rather than as successes, and
the nightly report distinguishes "never started" from "started and did not
finish" — previously both were filed under the same heading.

### One caveat, deliberately on the record

At the time of writing this fix is confirmed by inspection, **not** by
observation. All three recorded crashes happened before the currently-running
version was installed, and no maintenance window has run since. The first thing
that will genuinely confirm it is the next 5am.

That distinction is being written down rather than smoothed over, because a
merged fix plus a convincing explanation is exactly the combination that gets
recorded as "verified" when nothing has actually been verified yet.

## 23. The copies a merge left behind (Aug 23)

**Merging duplicate series could quietly abandon alternate copies of a book.**

When the library holds the same series twice under slightly different names, merging
them is meant to move every book across and then remove the empty one. It moved only
the books it could see — and it was deliberately not looking at alternate copies: the
second and third rips of a book you already own, which the library hides behind the
main one. Those were left pointing at a series that had just been deleted, which is how
a book ends up on the shelf with no series and nothing ever revisits it.

A second problem sat behind it. If moving one book failed partway, the series was
deleted anyway and the job reported success. That turned a recoverable hiccup into
permanent loss with a green tick next to it.

Both are fixed, along with two further mistakes found while reviewing the fix itself.

**No book is known to have been affected.** The jobs carrying this fault have not run on
this library in at least the last nine days, and the scheduled one has a prior
measurement of never having run at all. This was a trap that had not yet sprung.

It is worth recording anyway, for a reason that is easy to miss: the same mistake, in a
job that *was* running nightly, is exactly what section 17 above describes — the series
that vanished from under 13,322 books on August 14. That one did real damage. This is
the same class of fault caught before it fired, not a second helping of it, and the two
should not be added together.

One caution about the evidence, which applies beyond this entry. The reason we can say
these jobs did not run is a direct measurement of what actually ran. It is *not* the
absence of failure reports — and this month gave us a sharp lesson in why that
distinction matters. As the previous section describes, the nightly maintenance run
itself was dying on its first task, so nothing scheduled behind it executed at all. A
scheduler that is not running produces no failures. Silence from it was never evidence
that anything was well.

## 24. A "Rescan" button that never rescanned anything (Aug 24)

The book page had a button labelled **Rescan Files**. It did not rescan anything. What it
actually did was re-check how big each file on disk was and correct that number if it had
drifted — useful, but not what anybody pressing "Rescan" was asking for. Someone who
noticed a book's title or author was wrong and pressed it would have seen it complete
successfully and change nothing, with no way to tell why.

There was a second button, for the book's whole folder, that *did* force a genuine re-read.
Its problem was cost, and the cost was invisible. Folders in this library are not all the
same size: one of them holds 1,458 files directly. Pressing "rescan this book" could
therefore re-read 1,458 files, and nothing on screen said so.

Three changes went out together. The mislabelled button is now **Reconcile File Sizes**,
which is what it does. A new **Force Rescan** re-reads exactly one book — the machinery to
do that precisely already existed and simply had never been connected to a button. And the
folder button is now **Rescan Whole Folder**, with its real cost stated when you hover over
it. The old web address still works and still does the old thing, so anything already
calling it keeps behaving the same way rather than silently starting to do something else.

One honest limitation, recorded rather than papered over. "Force Rescan" is precise but not
immediate: it marks the book and the next scheduled scan picks it up, which can be up to six
hours later. The obvious way to make it immediate — letting a small targeted scan run
alongside the big one — turns out to be unsafe for a reason the project already learned the
hard way in early August: two jobs writing the same records at the same time silently lose
fields. So rather than ship something that looked faster and quietly corrupted data, the
three real options were written down for a decision. The most promising one is not a
workaround at all: make the full scan fast enough that waiting for it stops mattering.

Alongside this, the scan gained the ability to say how much work it skipped. It has always
avoided re-reading files that have not changed, but it never counted or reported that, so a
scan that skipped everything and a scan that skipped nothing produced identical logs. It now
reports the proportion skipped and, for the files it did re-read, which of five reasons
applied. That distinction matters: "the file changed" is the scan working correctly, while
"we have no record of this file" is a file being re-read on every single run forever. Those
need different fixes, and until now there was no way to tell which one you were looking at.

## 25. The same library, asked twice, answering differently (Aug 24)

The app can look through your library for authors that are probably the same person
entered twice — "Mikkelsen" and "Mikkelson", that sort of thing — and offer to merge
them. This month we found that if you ran that check twice on a library that had not
changed at all, you could get **different suggestions each time**.

Not merely a different order. Different groupings. Given four spellings of one surname,
one run might pair them off one way and the next run pair them another. Nothing about
your library had changed in between.

The cause was a small thing with a large shadow. When the check walked through the
surnames it had collected, it walked them in whatever order the computer happened to
hand them over — an order that is deliberately shuffled, and genuinely different on each
run. The grouping logic then works on a first-come basis: whichever spelling it happens
to see first becomes the one the others get folded into. Shuffled input, first-come
rule, different answer. Both parts now go in a fixed, sorted order, so the suggestions
depend only on what is actually in your library.

Why this matters more than a cosmetic wobble: these suggestions are the input to
merging authors together. A list that changes when the underlying facts have not is a
list you cannot check, cannot compare against yesterday's, and cannot safely act on in
bulk.

### The same check was also very slow

Comparing every surname against every other surname meant **26.4 million comparisons**
on this library's 7,261 distinct surnames — all on a single processor core while the
other nine sat idle. Three changes fixed that, none of which changes a single answer:

- **Not comparing what cannot possibly match.** Two names of very different lengths
  cannot score as similar, no matter how they are spelled — that follows from how the
  similarity score is defined, so it can be proven rather than guessed at. Checking
  lengths first is close to free and rules out **61%** of the comparisons before any
  real work starts.
- **Using the whole machine.** Deciding which names look alike is work that can be split
  across all cores safely, because it only reads and never changes anything shared. The
  grouping step that follows genuinely does depend on order, so that part deliberately
  stayed on one core.
- **Not re-doing work that never changes.** Each surname's length was being measured
  again on every single comparison it took part in — 52.7 million measurements to
  establish 7,261 facts that never change. And every comparison began by checking
  whether two surnames were identical, which cannot happen, because the list is built
  from names that are unique by construction. Both are gone.

**Result: 4.6 seconds down to 0.5 — about nine times faster** on a library-sized
test corpus, with the output checked
to be identical, character for character, to what the old code produced.

### One thing deliberately left alone

The new checks record an existing oddity rather than hiding it: given four spellings of
one surname, the app produces two pairs rather than one group of four. It never chains
"A matches B" and "B matches C" into a single group. That behaviour is unchanged by this
work — it was verified identical before and after — so it was written down rather than
quietly altered. It is now the kind of thing that will show up loudly if anyone changes
it, on purpose or by accident.

### A note on how this was found

This work also corrected an internal audit document from July that listed places in the
code doing this kind of unnecessarily slow work. Its five priority items had all in fact
been dealt with already, but the document still read like an open to-do list, which is a
good way to get someone to "fix" working code. The slow author check described above was
not on that list at all — the July sweep looked for work that loops over books and
authors, and this one loops over surnames, so it did not match the pattern being searched
for. Both facts are now recorded in the document itself.

One correction to that correction, which is worth being straight about. An earlier draft
of this section, and of the document itself, said *every* item on the list had been
fixed. That was not true: alongside the five specific items there was a sixth,
open-ended one — "everything else" in two longer tables — and those tables were never
re-checked. At least two places named in them are still doing this work one item at a
time today. A document written to stop people trusting a stale claim had a fresh
overstatement of its own in it, which is a fair illustration of how easily this happens.
Both now say plainly which parts were verified and which were not.

## 26. The one way of changing a book's files that forgot to update the book (Aug 24)

A book shows a total running time and a total size. Those are not stored facts —
they are sums of the individual files underneath, and they have to be re-added
whenever the files change. Adding, editing and deleting a file all did that
afterwards. The bulk path did not.

The bulk path is the one used when the system rewrites many files at once, which is
exactly what a library-wide repair job does. It wrote every file correctly and never
re-added the totals. So the parent book kept whatever number it had from the last
single-file edit, or from the day it was first imported.

Nothing announced this, because from the system's point of view nothing failed. The
write succeeded. The files were right. Only the two summary numbers on the book were
quietly out of date. The unpleasant consequence is what it does to a repair job: a
run that correctly fixes every file it touches finishes, reports success, and leaves
the books still displaying the old totals. Checking afterwards makes the repair look
like it did nothing — which invites running it again, to the same non-effect.

One repair job had already hit this and worked around it privately: the duration
repair does its own second pass to re-add the totals, with a note explaining that the
bulk path would not do it. So the gap was known, in one place, by one author, and
never fixed where it lived — every other user of the bulk path kept the bug.

That workaround is being kept rather than removed, for a reason worth stating: it is
the only place where a failure to re-add the totals is reported loudly enough to stop
the job. The bulk path's version does its best and writes a warning if it cannot. So
the two are not duplicates — one is the fix, the other is the alarm.

The bulk path now re-adds the totals once per affected book, after the write lands.
Once per book matters: re-adding the totals means re-reading all of that book's
files, so doing it once per file turns writing a 200-file audiobook into roughly
twenty thousand reads instead of two hundred — the same shape of problem as the one
in section 25, in a different part of the system.

Fixing that then exposed a second problem sitting right behind it: **the scanner was
erasing the totals it had just computed.** When it sets up a book's files it loads the
book once at the start, writes the files, and then writes that original copy back in
order to tidy up the book's stored location. That copy still holds the totals from
before the write — and the save routine, which protects nine other fields from being
blanked this way, does not protect these two. So the numbers were calculated correctly
and then overwritten with nothing, inside a single operation, for every single-file
audiobook imported. The scanner now re-reads the book before that last save, and says
so in the log if it cannot.

This is worth noting as a pattern rather than an incident: the first fix was correct and
would still have appeared to do nothing on the most common path, because something
downstream quietly undid it. Shipping it without checking the callers would have counted
as a success.

**A note on what was and was not measured here**, because it is easy to run two
findings together. Production logs were examined, and they identified a *different*
and larger instance of this same waste: one repair job, re-adding a book's totals
once per file it creates, accounting for the overwhelming majority of the observed
recalculations. **That one is not fixed by this change and is still open.** The bulk
path described above was found by reading the code, not by that measurement — in
fact the measurement specifically ruled it out as a source, precisely because it
never re-added the totals at all. Two related problems, one fixed here; the
measurement belongs to the other one.

Two smaller things were settled while confirming the fix, both of which had been
easy to assume wrong:

- When a file is moved or reassigned, it can end up belonging to a different book
  than the one that submitted it. The book that gets its totals refreshed is now
  guaranteed to be the one the file actually landed on.
- Deleting every file from a book does **not** reset its running time to zero. That
  is deliberate — a file that has temporarily gone missing should not be allowed to
  erase a duration that was expensive to determine. A note in the code claimed the
  opposite, confidently enough to plan against; it has been corrected and the real
  behaviour written down as a test.

And one further gap was found while checking the above, **not yet fixed**: moving
files from one book to another updates neither book's totals. Because it moves
running time and size *out* of one book and *into* another, both are left wrong
afterwards — the source still counting what it no longer has, the destination not
counting what it now does. It has eight callers, including the merge and version-swap
paths. It is recorded for a follow-up rather than folded into this change, since it is
a different method with a different set of callers to check.

## 27. Sorting the library mostly did nothing (Aug 25)

The library page offers 23 ways to sort. **Ten of them, plus three alternate spellings
of the same options, did not sort at all.** Choosing "sort by author" returned the books
in whatever order they happened to be stored in. So did sorting by series, year, genre,
language, publisher, format quality, edition, bitrate and sample rate. Sorting by title,
narrator, length, file size and file type worked, and always had.

Nothing about the page suggested a problem. The request succeeded, the right books came
back, the count was right — only the order was meaningless. There was no error to notice
and no warning to read, which is why this survived so long unreported.

The cause was three separate pieces of the system each behaving reasonably. The part that
receives the request only ever told the storage layer about *one* of the sort options, so
the storage layer never learned which sort the reader had asked for. The storage layer,
for its part, assumed that whoever asked would do the sorting. And whoever asked could
not: by the time the books reached it, they had been trimmed down to a summary that no
longer carried the author, the genre, the year, or most of the other things being sorted
on. Each piece was individually correct. The gap was in the space between them, which is
exactly where no one was looking.

There is a detail here worth stating plainly, because it makes the failure worse than it
first sounds. When two books tie in a sort, the system falls back to putting them in the
order they were added. That is a sensible rule. But when *every* book ties — which is what
happens when the thing being sorted on is missing — that fallback quietly reorders the
whole list. The storage layer had, in some cases, already produced a correctly sorted
list; the final step then threw that away and replaced it with "order added". A cruder
implementation would have left the correct answer alone.

All 23 options now sort, including on later pages: asking for the second page of books by
author now returns the books that genuinely belong there, rather than an arbitrary handful
arranged neatly.

Two things were checked rather than assumed. First, every one of the 23 options was tested
individually, through the same path the app uses, and each was confirmed to produce the
right order — the fix was not declared done on the strength of the two or three that were
easy to check. Second, the new tests were deliberately broken, one at a time, to confirm
they would actually notice if the bug came back. Four out of five broke the tests as
intended. **The fifth did not**, and that gap has been closed with an additional test. A
test that has never been seen to fail is not yet known to work.

That last point deserves emphasis, because the existing tests are the reason this shipped.
Two of them looked like thorough sorting tests. They checked that sorting the same list
twice gives the same answer, and that sorting does not lose or duplicate any books. Both of
those things are perfectly true of a sort that does nothing whatsoever. The tests were not
weak or careless — they were simply asking questions that the broken behaviour could answer
correctly.

## 28. The release that could not be cut (Aug 25)

Alongside the above, the process for publishing a finished version of the application was
repaired. Release candidates — the interim builds made on the way to a finished version —
had been accumulating without ever being cleared out: **271 of them, against 955 leftover
version markers.** More seriously, the step that publishes a *finished* version had been
failing silently, so no finished version could be issued at all.

Two things were blocking it. A previously abandoned attempt had left an empty placeholder
holding the version number in reserve, so every later attempt found the name already taken.
And a repository-wide rule intended to protect version markers from deletion was also
preventing the routine cleanup of interim ones, which is how they had grown to 271 in the
first place.

The backlog was cleared to 13, and version **v0.219.2** is now published with all four
platform builds and their verification file attached.

Worth recording honestly: this was confirmed by checking that the published version
actually has the downloadable files attached to it, not merely that the publishing job
reported success. An earlier check in the same session reported the job as failing when it
was in fact still running, and the finished-version marker that had blocked everything had
itself looked like a successfully published release. In both cases the summary was
misleading and the underlying detail was not. The protective rule remains switched off
pending a decision on the narrower exemption it needs.

## Themes worth carrying into next month

1. **A silent fallback is worse than a loud failure.** The transcription outage, the
   empty library page, and the search index's "run without search if it cannot open"
   behaviour are the same shape: something degrades, nothing says so, and the result is
   indistinguishable from normal.
2. **A measurement is only as good as the thing it ran against.** The test suite reported
   green three separate ways this month — hidden output, a stale server, and a
   developer machine that was not the build server. Each looked like evidence. A fourth
   turned up on 23 August and is the sharpest of them: a test that invented the input it
   was testing against, and so spent months confirming the code did what the test assumed
   the outside world does. The first three were checks that ran in the wrong place; this
   one ran in the right place and certified the bug.
3. **Being wrong on the record is fine; leaving it there is not.** Three summaries this
   month correct earlier ones. That is the system working.
4. **A comment that records a fact can go stale faster than code that records a rule.**
   The collections work justified a decision with "we checked, the conflicting thing
   does not exist" — true when written, false one commit later, in the same change. A
   check that runs is worth more than a check that was run once and written down.
   The nightly-maintenance crash on 23 August is the same lesson twice over. The note
   attached to the change that caused it listed exactly where its new object was used —
   and the list was already missing the one caller that would go on to crash. And
   somewhere else in the same area, a second note described the resulting hazard
   precisely, by name, and then routed only its own code around it, leaving the shared
   version broken for everyone else. Writing a warning down is not the same as acting on
   it, and fixing your own path is not the same as fixing the path.
5. **A check that exists is not a check that runs.** Three separate examples surfaced on
   20 August alone: a guard on the plug-in toolkit that ran only on developers' own
   machines, a body of code kept behind a switch that no build ever turned on, and a code
   formatter verified in neither place. Two of them had been reporting failure for
   thirty-three days and four months respectively, the entire time on a green board.
   Writing a new check is the easy half; the project has no rule that one must be
   reachable by the automated system before it counts, and until it does, the green tick
   answers a narrower question than anybody reading it assumes.
6. **A test can ask a question the bug answers correctly.** The sorting failure on 25
   August shipped past two tests that looked like thorough coverage of sorting. They
   checked that sorting twice gives the same answer, and that sorting loses no books.
   Both are true of a sort that does nothing at all. This is a different failure from the
   four above: those were checks that did not run, or ran in the wrong place. This one ran,
   in the right place, and passed honestly — it was simply not asking whether anything got
   sorted. The useful habit is to ask, of any test, what a do-nothing implementation would
   do to it. If the answer is "pass", the test is describing something other than the thing
   that matters.
