<!-- file: docs/executive-summaries/2026-08-11-instructions-that-were-thrown-away-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f8b1d47-6c93-4e05-a71b-3d9c8e04f625 -->
<!-- last-edited: 2026-08-11 -->

# Executive Summary: The Instructions That Were Thrown Away

**Date:** 2026-08-11
**Change:** PR #2309 (with #2308, #2310 alongside it)
**Written for:** anyone who uses the audiobook organiser, not the people who build it

---

## In one paragraph

When you told the organiser to scan, organise, or convert a specific set of books, it
threw your instructions away and did the job on **the entire library instead**. It did
not fail, warn, or log anything. It accepted the request, reported success, and did
something reasonable-looking at completely the wrong scale. This had been happening for
an unknown length of time — long enough that nobody remembered it working any other way.

---

## What you would have noticed

Probably this: you selected a handful of books, pressed Organise, and the job ran for
far longer than a handful of books should take. Or you set an option — "fetch metadata
first", "only this folder" — and it made no difference. Or the Convert button simply
never worked at all.

All three were the same defect.

---

## What was actually happening

Every instruction you send travels from the button you pressed to the part of the
program that does the work. On that journey it has to be packed up and unpacked again.

The packing step was using the wrong method. It produced a package that was perfectly
valid, just unreadable at the other end — the equivalent of handing someone a sealed
envelope when they were expecting a spoken instruction. Technically a delivery. Contains
nothing they can act on.

The unpacking step opened the envelope, found something it could not read, and
**silently gave up**, falling back to "no instructions given."

That fallback is where the damage was done. To the organiser, *"no books were specified"*
does not mean "do nothing." It means **"no restriction was requested — so do all of
them."** An empty instruction was read as a request for everything.

---

## Why nobody caught it

Three things had to line up, and they did.

**Two faults cancelled each other out.** The sender packed wrong; the receiver hid the
complaint. Either one alone would have been loud and obvious — a visible error, an
immediate crash. Together they produced a job that started, reported progress, returned
success, and did something plausible. Silence is the worst possible symptom, and it took
two separate bugs to manufacture it.

**There was a test covering exactly this, and it passed the whole time.** It checked
that an instruction *was sent*. It never checked *what was in it*. It went green on every
run for the entire life of the defect, which is worse than having no test at all — a
passing test with the right name is a reason not to look.

**The evidence was sitting in plain sight.** The Convert function was stricter than its
two siblings: given an unreadable instruction, it refused outright instead of guessing.
So Convert had been **visibly broken this whole time**, right next to Scan and Organise,
which were quietly doing the wrong thing. One faulty line, three functions, two
completely different symptoms — and the loud one was the clue to the quiet ones.

---

## What changed

The packing method was corrected — a one-line change.

More importantly, the unpacking step **no longer hides its complaints**. If an
instruction cannot be read, the job now stops and says so rather than inventing a
default. That was applied in thirteen places, not just the one that caused this.

Two related changes shipped the same day: jobs that previously reported success after a
failed recovery now report the failure (#2308), and requests whose contents cannot be
read are now rejected rather than treated as "do everything" (#2310).

---

## What to expect now

**These functions will behave differently, and that is the fix working.**

Anything that appeared to work only because it was silently running across your whole
library will now run on the scope you actually selected. If a job that used to take
hours now takes minutes, nothing has broken — it is finally doing what you asked instead
of everything.

---

## What is not claimed

This has **not been confirmed on the live server** as the *only* reason organise jobs ran
library-wide. The mechanism is proven and sufficient on its own; verifying there are no
other contributing causes needs a working login on the production system, which was
unavailable when the fix shipped.

Existing records are not repaired retroactively. The fix stops the loss going forward; it
does not reconstruct instructions that were already discarded.

---

## The lesson worth keeping

A discarded error message is not just a lost message. It is a **shock absorber** that
lets a completely separate fault upstream survive indefinitely without anyone noticing.
Removing it is what turns a silent wrong answer into a visible failure — which is the
entire point, even though it means things start failing loudly that appeared fine before.

When a correctness fix suddenly makes something break, the first question is not "what
did I break?" It is **"what was that error hiding?"**
