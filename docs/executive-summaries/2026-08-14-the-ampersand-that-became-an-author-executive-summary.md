<!-- file: docs/executive-summaries/2026-08-14-the-ampersand-that-became-an-author-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: c9147e3b-8a62-4d05-b7f1-2e6a09d84c53 -->
<!-- last-edited: 2026-08-14 -->

# Executive Summary: The Ampersand That Became An Author

**Date:** 2026-08-14
**In one line:** 48 people in your library were filed twice — once under their name, and
once under their name with a stray "&" stuck to the front — because of how a list of
narrators got chopped up.

---

## What you saw

Author pages listing `& India Fisher` and `& Lisa Bowerman` as if they were people. Not
an error message, not a crash — just names that were subtly wrong, sitting quietly in the
author list next to the correct spelling of the same person.

## What was actually happening

Audiobook files carry a credit line naming everyone involved. The one on
*The Creed of the Kromon* reads:

> Paul McGann, India Fisher, & Conrad Westmaas

The library has to turn that one line into three separate people. It does that by
chopping the line at its punctuation. It tried the comma first, which gives:

> `Paul McGann` | `India Fisher` | `& Conrad Westmaas`

The first two are fine. The third kept the ampersand, because the chop happened at the
comma and nothing afterwards looked at what was left over.

The library *did* have a rule for handling ampersands. It just never got to run. The
rules are tried in order, and the comma rule went first — so for exactly the credit lines
the ampersand rule was written for, it was unreachable.

**The tell was the punctuation used.** Writing "A, B, & C" — with a comma *before* the
ampersand — is the only style that strands the symbol like this. Written "A, B & C" the
same code produces `B & C`, which is obviously broken and would have been spotted
years ago. The tidier punctuation produced the quieter bug.

## Why nobody noticed

Two reasons, and both are worth knowing.

**It only happened once.** All 48 bad entries came from a single import of one audio
drama collection. No new ones appeared afterwards — but that is because nothing has
re-imported credits in that style since, not because anything was fixed. The next import
of that shape would have made more.

**Nothing looked broken.** A person filed twice does not throw an error. Their books just
split across two entries, so one shows fewer books than they should have, and the other
shows a name with a stray symbol. Everything continues to work.

## What was fixed

Two separate things, because stopping the bleeding and cleaning up the mess are not the
same job.

**The chopping rule.** The fix went into the one step every chopping rule shares, rather
than into the comma rule alone — the same flaw was sitting in the rules for slashes,
semicolons and brackets, and patching only the one we caught would have left three copies
of it behind to be rediscovered later.

**The 48 existing entries.** A new cleanup task handles them, and it does two different
things depending on what it finds:

- **31 of them** already have a correctly-spelled twin elsewhere in the library. Their
  books move over to the real person, and the bad entry goes away.
- **The other 17** have no twin. Those simply get renamed in place, which keeps every
  book already attached to them attached.

The cleanup reports what it *would* do before it does anything. Merging deletes entries,
so that plan is meant to be read first.

## The part that was deliberately left broken

Three entries look like the same bug and are not:

- `and Thanks for All the Fish`
- `and the Farm Boy (DBY)`
- `and Make Better Decisions`

These are fragments of **book titles** that ended up in the credit line and got chopped
the same way. *So Long, and Thanks for All the Fish* is a Douglas Adams novel, not a
person.

The cleanup could easily have stripped the "and" from these too. It deliberately does
not. Removing it gives `Thanks for All the Fish` — still not a person, but no longer
*obviously* wrong, and therefore much harder for anyone to find later. Leaving them
visibly broken is what keeps them findable. They are recorded for a separate fix that
addresses the actual problem: the chopping rule cannot tell a person's name from a
fragment of a title.

The same reasoning protects two other entries, `&#169` and
`&#169;2013 by HarperCollinsPublishers` — leftovers of a copyright notice that landed in
a credit line. A careless cleanup would have turned the first into `#169`. It is left
alone too.

## How we know it is actually fixed

The test was written using the real credit line read off the actual file on disk, not an
invented example — and it was run **before** the fix and watched to fail, on all four
chopping rules, so we know it is capable of catching the problem rather than just
agreeing with the code.

The two safety rules in the cleanup were checked the same way: each was deliberately
broken to confirm the tests go red. Widening the cleanup to include "and" produced
exactly the laundering described above, which is the evidence that the narrow version is
doing real work.

---

**Scale:** 48 author entries, 146 books between them, out of roughly 9,350 authors.
**Blast radius:** author pages, author counts, and duplicate-detection candidates.
**Data loss:** none. Nothing was deleted; entries were duplicated, not destroyed.
