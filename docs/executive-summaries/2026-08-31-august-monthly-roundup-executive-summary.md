<!-- file: docs/executive-summaries/2026-08-31-august-monthly-roundup-executive-summary.md -->
<!-- version: 1.6.0 -->
<!-- guid: e7a3f109-52d8-4c6b-91f4-08b7c2d64e35 -->
<!-- last-edited: 2026-08-18 -->

# Executive Summary: August 2026 Monthly Roundup

**Period covered:** 2026-08-01 through 2026-08-18 (**month in progress** — this is
updated as work lands, not a closed record).
**Individual write-ups this consolidates:** the seven dated summaries in this directory
from 2026-08-04 to 2026-08-09, linked inline below.

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

## Themes worth carrying into next month

1. **A silent fallback is worse than a loud failure.** The transcription outage, the
   empty library page, and the search index's "run without search if it cannot open"
   behaviour are the same shape: something degrades, nothing says so, and the result is
   indistinguishable from normal.
2. **A measurement is only as good as the thing it ran against.** The test suite reported
   green three separate ways this month — hidden output, a stale server, and a
   developer machine that was not the build server. Each looked like evidence.
3. **Being wrong on the record is fine; leaving it there is not.** Three summaries this
   month correct earlier ones. That is the system working.
4. **A comment that records a fact can go stale faster than code that records a rule.**
   The collections work justified a decision with "we checked, the conflicting thing
   does not exist" — true when written, false one commit later, in the same change. A
   check that runs is worth more than a check that was run once and written down.
