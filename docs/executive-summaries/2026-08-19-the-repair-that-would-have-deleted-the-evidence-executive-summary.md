<!-- file: docs/executive-summaries/2026-08-19-the-repair-that-would-have-deleted-the-evidence-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b47f2e91-6c30-4d85-a1f9-72e8c0d4b3a6 -->
<!-- last-edited: 2026-08-19 -->

# The repair that would have deleted the evidence

**2026-08-19 — why the "fix missing files" job was about to destroy the only clue
pointing at files that are sitting safely on disk, and what it does now instead**

## The short version

There was a cleanup job whose purpose was to tidy up the library's list of audio files
by removing entries that pointed at files no longer on disk. It was never allowed to
run — it needed your approval first, and that approval was never given.

**That turned out to be the thing that saved the files.** The entries it wanted to
remove are, in a large number of cases, the only record that a real audio file exists.
The file is genuinely there. It just has a slightly different name than the entry says.
Delete the entry and the file becomes an anonymous orphan on disk that nothing in the
library knows about.

The job no longer deletes anything. It reports, and brings the problem to you.

## 1. How the names got broken

Every audio file in a book gets a name built from a template. For about five and a half
months — from early March to mid-August this year — the template that shipped by default
put the track number in as **"70/131"**, meaning "track 70 of 131".

The slash is the problem. On disk, a slash does not mean "of". It means "go into a
folder". So a file that should have been saved as:

> Zero History **- 70.mp3**

was instead recorded as:

> Zero History **- 70** ⟶ **131.mp3**

— a folder named "Zero History - 70" with a file called "131.mp3" inside it. Neither of
those is a real thing. The name was nonsense, and the library wrote the nonsense down.

The actual files on disk were repaired at some point. **The library's list of them was
not.** So the list is full of entries pointing at these phantom folders, and every one of
those entries looks, to a cleanup job, exactly like a dead entry worth removing.

## 2. Why removing them would have been the worst possible move

When this was checked against the real disk, **every single one of the 101 broken entries
examined had its audio file present** — sitting there under the sensible flat name, fully
playable, exactly where you would expect it.

So the entry is not garbage. It is a slightly wrong pointer at a file that exists. It is
the *only* pointer at that file. Removing it would have:

- taken a book that is one rename away from working, and
- turned it into a book that is missing a file, with an untracked audio file left behind
  on disk that nothing would ever look at again.

A wrong index is recoverable. A lost file is not. The old job would have converted the
first into the second, thousands of times over, in a single unattended run.

## 3. What changed

**The deleting is gone — not switched off, removed.** The code that performed deletions
has been deleted from the program, along with the permission it used to hold and the
internal plumbing it used to reach the database. Turning it back on is no longer a matter
of flipping a setting; someone would have to write it again from scratch and explain why
in review.

**Asking for the old behaviour now fails loudly.** If anything still asks the job to
"apply" its changes, it gets a clear error explaining that this job never deletes, and
pointing at the findings that led to the change. It does not quietly pretend to succeed.

**The job now reports.** It checks every file entry, groups the dead ones by book, and
tells you what needs a decision — which is what you asked for.

## 4. Answering "how many", properly

The obvious next question is: how many books are affected? The honest answer until now
was **we do not know**, and the way the earlier check worked, we could never have found
out by simply looking harder.

The reason is worth stating plainly, because it is a trap. The earlier check looked at a
**sample** — but it took that sample by grabbing the first several hundred entries in the
order the database happened to hand them over. Files belonging to the same book sit next
to each other in that order. So the sample was not a cross-section of the library; it was
a handful of books examined in depth. Looking at a bigger sample the same way just gives
you *more books examined in depth* — never a percentage that applies to the whole library.

So a new check was added that goes over **every** affected entry, works out where the file
would be under its sensible name, and asks the disk whether it is there. It reports three
numbers: how many are recoverable, how many genuinely lost their file, and how many do not
match this pattern at all.

It is switched off unless you ask for it, because checking every entry means asking the
network storage a very large number of questions.

### The check checks itself

Mixed in with the real questions, the new pass deliberately asks about **two files that
cannot possibly exist**. If the disk ever answers "yes, that one's here", something is
badly wrong — the wrong drive is connected, or a bug is making everything look present —
and the entire run is thrown away rather than reported.

Without that, a result of "every file we looked for was found!" would be indistinguishable
from a broken check that says yes to everything. It is the difference between a
measurement and a number that merely looks like one.

## 5. What has not been done

**The actual repair does not exist yet.** Nothing has renamed or re-pointed anything. This
work made the situation safe and measurable; it did not fix it.

When that repair is built, it must **re-point** each entry at the file that is really
there — never delete. That instruction is now written at the exact spot in the code where
someone would go looking.

The **16,265 books where every single file entry is dead** are still untouched and still
need your decision. They are now structurally impossible to touch by accident.
