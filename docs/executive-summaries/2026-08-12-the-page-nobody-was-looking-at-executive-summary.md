<!-- file: docs/executive-summaries/2026-08-12-the-page-nobody-was-looking-at-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6708f45d-5cfa-4d76-9ac2-881672f5288b -->
<!-- last-edited: 2026-08-12 -->

# Executive Summary: The Page Nobody Was Looking At

**Date:** 2026-08-12
**Change:** PR #2318
**Written for:** anyone who uses the audiobook organiser, not the people who build it

---

## In one paragraph

The server kept dying. Not slowly, not with a warning — it would run out of memory and
be killed outright, taking the whole library offline, five separate times. The cause was
the **Activity page**: the screen that shows you what the organiser has been doing
lately. Opening it started a job that read the entire activity history into memory at
once. If you gave up waiting and closed the tab, that job **kept running anyway**. Every
time you reopened the page, another copy started. Thirty of them were running at the
moment the server died, with nobody connected to the server at all.

---

## What you would have noticed

The server becoming unusable, and no obvious reason why. You would open the Activity
page, it would sit there loading, you would give up and close it or hit refresh. A few
minutes later the whole site would stop responding. When it came back, everything looked
fine — until the next time you checked Activity.

The connection between the two was invisible, and deliberately so from the software's
point of view: the organiser only writes a line in its log when a request **finishes**.
A request that never finishes never gets logged. So the one screen that was killing the
server was the one screen that left no trace of having been opened.

---

## What was actually wrong

Three faults stacked on top of each other. Any one alone would have been survivable.

**It read everything.** Asking for the last 25 activity entries did not fetch 25 entries.
It read the whole history, decoded all of it, sorted all of it, and then handed back the
first 25. The library has years of history. Reading it once cost roughly 9 gigabytes of
memory.

**It never gave up.** When you close a browser tab, the server is told. This job was not
listening. It carried on reading and allocating memory for a page that no longer existed
and a person who had already walked away.

**Nothing limited how many could run at once.** Because each one was slow and none of
them stopped, they piled up. Reopening the page five times meant five copies. Thirty
copies is what it took to exhaust a 30-gigabyte allowance.

The decisive piece of evidence was a memory snapshot taken while the server was dying: it
showed thirty of these jobs still working, holding 30.8 gigabytes between them — while a
separate check of the network showed **zero people connected**. Every single one of them
was working on a page nobody was looking at.

---

## What was done

The job now reads newest-first and stops as soon as it has enough to answer your
question. Asking for 25 entries reads roughly 25 entries. It checks regularly whether you
are still there, and abandons the work immediately if you are not. A second, smaller
piece of the same screen — the dropdown listing which parts of the system have been
active — now reuses its answer for 45 seconds instead of recalculating it from scratch on
every single request.

One change is worth describing because it prevents the mistake coming back rather than
just repairing it. The old code had two ways to ask for activity data: one that could be
cancelled, and one that could not. The convenient one was the one that could not, so that
is the one every screen used. **The uncancellable version has been deleted entirely.**
There is now no way to write this bug again — not "a rule against it", not "a warning in
a comment", but no available route to it.

---

## What this does not fix

**The activity history is still large.** Compacting it — the routine tidy-up that
discards old detail — has the same read-everything-at-once problem, and running it today
would repeat the outage. It has been deliberately left alone and written up. Do not
trigger a log compaction until that is fixed too.

**Whether this was the only cause has not been proven.** Five crashes were observed; the
memory snapshot explains the one that was captured. It is reasonable to expect the others
were the same, but that is an expectation, not a measurement.

---

## Where the server stands now

Deployed and verified on 2026-08-12. The Activity page, which previously consumed
gigabytes and could take the server down, now answers in **55 milliseconds**. The
identity of the running program was confirmed against the exact version of the code
containing the fix, rather than assumed from a successful deployment message.

---

## The uncomfortable part

This bug was not subtle once it was found, and it had been there a long time. What kept
it hidden was that every individual signal pointed away from it. The server logs showed
nothing, because unfinished requests are not logged. The page appeared to be merely slow,
which reads as an annoyance rather than a danger. And closing the tab *felt* like
cancelling — the visible thing stopped, so the invisible thing was assumed to have
stopped too.

The general lesson, which applies well beyond this one screen: **giving up on a request
is not the same as the request giving up.** Anything the software starts on your behalf
has to be told when you stop caring, and it has to be built so that it cannot ignore
being told.
