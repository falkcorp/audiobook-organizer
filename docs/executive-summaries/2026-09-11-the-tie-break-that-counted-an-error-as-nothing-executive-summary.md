<!-- file: docs/executive-summaries/2026-09-11-the-tie-break-that-counted-an-error-as-nothing-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 099605bd-b989-4be6-944d-02a8d38e919f -->
<!-- last-edited: 2026-09-11 -->

# The tie-break that counted an error as nothing

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3226

## Executive Summary

When the app applies a catalog match to a book, it checks whether any other
book in the library was matched to the exact same catalog record. If so, the
two are the same title, and the app quietly folds them together: the copy with
the most audio files is kept as the main entry, and the others are marked as
duplicates of it and stop showing up on their own.

To decide which copy is "the one with the most files", the app asks the
database how many files each copy has. Until now, if that question failed for
a moment — a busy disk, a timed-out read — the app treated the answer as
**zero**. That is the same answer a copy with no files at all would give. So a
short hiccup on the copy that really had the most files could make it lose the
contest to a copy with fewer files, or none, and the whole group was then
rearranged around the wrong book. Nothing was written to the log to say why,
so anyone looking into a bad merge afterwards had nothing to go on.

The fix changes the order of operations. The app now gathers every fact the
decision depends on before it decides anything. If any of those reads fails,
the decision is called off for that group: no book is marked as a duplicate,
the failure is recorded in the log in plain terms, and the catalog match
itself still goes through. The check simply runs again the next time a match
is applied to any book in the group, by which point the read normally
succeeds. The rule for picking the winner — most files, and the older entry on
a tie — is exactly as it was.

A test now reproduces the original mistake: with the biggest copy briefly
unreadable, the old code demoted it under a one-file copy; the new code
refuses to decide and leaves the library untouched.
