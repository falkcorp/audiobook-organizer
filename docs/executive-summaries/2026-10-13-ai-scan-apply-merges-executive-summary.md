<!-- file: docs/executive-summaries/2026-10-13-ai-scan-apply-merges-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3c8e5b71-9a24-4d06-b7f3-6e1d2a94c850 -->
<!-- last-edited: 2026-10-14 -->

# The AI scan's "Apply selected" button now merges authors, and merges respect locked authors

Branch `task/03-pr8` (03-PR8, PR #4013).

## Executive Summary

- **Background.** The AI author scan finds author names that are probably the
  same person ("J. R. R. Tolkien" and "Tolkien, J.R.R."). The review page lists
  its suggestions, and you tick the ones you accept and press Apply selected.
- **What was wrong.** Apply selected did not merge anything. It only ticked the
  results off as done. A reviewer who worked through a scan came away believing
  the duplicates were merged, while the library was unchanged and the scan's
  list showed nothing left to do.
- **The fix.** Apply selected now merges the authors, one suggestion at a time,
  and ticks a suggestion off only when its merge fully worked. Anything that
  did not work stays on the list and is named, with the reason, on the page.
- **Locked authors are respected.** When you lock a book's author, you are
  saying "do not change this". Merges now honour that lock, in the AI scan and
  in the Authors view's own Merge buttons. A locked book keeps its author, the
  author it is credited to is not deleted while that is so, and the lock is
  named in the report. Before this, a merge could quietly move a locked book to
  another author, which no one would notice until the credit looked wrong.
- **Each suggestion says what it will do.** A merge row reads "keep X, merge Y,
  Z" with names, so you can see which author survives before you press Apply.
  Only merge suggestions can be applied here; renames, aliases, splits and the
  like are shown read-only with a link to the Authors view.
- **Two scans cannot run at once.** Starting an AI scan while another is still
  running is now refused by the server itself, with a plain message. Before,
  two quick clicks could queue two paid scans.
- **Merges can be undone.** A merge used to be one-way: the merged-away author
  was deleted and nothing recorded how to bring it back. Now each step is
  written to the undo history before it happens. Undoing the operation brings
  the author back under the same name with its aliases, and puts every book's
  author credits back as they were. A book you edited after the merge is left
  alone and reported, and the undo is refused if another author has taken the
  name since. If the history entry cannot be written, that book is simply not
  moved. This covers the AI scan's Apply and the Authors view's Merge. Merges
  made before this change stay unrecoverable.
- **A narrator is no longer merged into the author.** The AI scan flags some
  names as narrators. One of the two ways it reports that was being dropped, so
  the apply merged the narrator's books into the author. The flag is now kept,
  and a result it cannot be matched to is refused instead of guessed at.
- **A scan being applied cannot be replaced under you.** A newer scan (the
  nightly one, for instance) could hide the scan you were applying part way
  through, leaving merged results unticked. The scan is now held while the
  apply runs.
- **A failed scan no longer blocks the next one.** A scan that failed to start
  stayed "running" and refused every new scan until someone cancelled it. It is
  now ended, and any paid batch it had started is cancelled.
- **Verification.** The new behaviour is covered by tests against a real
  library store: locked books stay put, only successful merges are ticked off,
  a second Apply of the same suggestions changes nothing, simultaneous scan
  starts produce exactly one scan, and a merge followed by its undo restores the
  author, its books' credits and their primary author.

## A button that recorded work it never did

**What it was.** Apply selected updated a flag on each selected suggestion and
stopped there. No author was merged, relinked or deleted.

**Why it mattered.** The scan's list is the only record of what is left to
review. With the flag set, the suggestions disappeared from the to-do list
whether or not anything had happened, so real duplicates were hidden from the
people who would have fixed them.

**The fix.** Applying now queues a background job that performs each merge with
the same code the Authors view uses, then sets the flag only for suggestions
whose merge succeeded. A suggestion that names a narrator or publisher it
cannot identify is refused rather than guessed at, because merging would have
folded the narrator's books into the author. The job previews unless told
otherwise, like the library's other jobs that change data.

## Merges that ignored locked authors

**What it was.** A book can have its author locked so automatic processes leave
it alone. The author merge never checked that lock.

**Why it mattered.** Merging many suggestions with one click could move locked
books to a different author, undoing a decision the owner had made on purpose.
The change is silent: the book simply shows another name afterwards.

**The fix.** The merge now checks the lock for every book it would move. Locked
books stay with their author, that author is kept while they do, and the report
lists each one. An owner who really wants the old behaviour can ask for it
explicitly.

## Two paid scans from a double click

**What it was.** The page disabled Start while a scan was running, but only the
page knew; the server accepted every request, and a nightly job also records
scans.

**Why it mattered.** Each AI scan of the whole library is a paid call, and a
double click or a second browser tab could start two.

**The fix.** The server now refuses a second start while a scan is unfinished,
and the check and the creation happen as one step so two simultaneous requests
cannot both pass. The nightly job is never refused, because the results it
records have already been paid for.
