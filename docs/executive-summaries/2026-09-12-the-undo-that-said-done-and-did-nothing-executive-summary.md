<!-- file: docs/executive-summaries/2026-09-12-the-undo-that-said-done-and-did-nothing-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7d2e9a41-3c6b-4f85-9e10-b4a7c3d8f256 -->
<!-- last-edited: 2026-09-12 -->

# The Undo that said "done" and did nothing

## Executive Summary

Most operations keep a list of every change they make, so that pressing
**Undo** can put things back. Each entry on the list is ticked off once it has
been undone.

Some clean-up operations delete things that have no undo yet: removing authors
with no books, merging duplicate authors, and (soon) removing narrators with no
books. They still write an entry for each deletion, as a record of what was
removed. The Undo feature did not know how to reverse these entries. It
reported an error for each one, **and then ticked off every entry on the list
anyway**.

So pressing Undo on the empty-author clean-up would have reported the whole
operation as undone while no author came back. The operation's history would
have said "undone" from then on. The clean-up that ran in production this week
wrote 1,742 of these entries, so anyone could have hit this today.

### What changed

- Undo now ticks off only the entries it actually put back. An entry it could
  not reverse, or one whose undo failed, stays unticked.
- If nothing in an operation can be undone (the author clean-up, for example),
  Undo refuses and says why: "this operation's changes are a record only and
  cannot be undone automatically: 1,742 author_delete rows". Nothing is changed.
- If only some entries can be undone, Undo puts those back and reports how many
  were restored and how many could not be, by type.
- The Undo button and the Activity Log now show that report. They no longer
  display "Operation reverted successfully" for a partial result.
- Before you confirm, the Undo prompt now counts only the entries that can
  actually be put back and names the ones that cannot. When none can, it says
  so and does not offer Undo. It used to ask "Undo 1742 change(s)?" for the
  author clean-up and then refuse.
- Merging duplicate authors also records which author each book pointed to.
  Undo cannot put that back yet, so those entries now count as records too, and
  Undo refuses with an explanation instead of reporting an internal error.
- If an earlier Undo only got partway, pressing Undo again now retries the
  entries that were left, instead of claiming the operation was already undone.

New tests cover a clean-up with only deletion records, a mix of reversible and
record-only entries, and the narrator version of both. The same tests fail on
the code before this fix. Further tests cover the Undo prompt's count, the
author-merge entries, and retrying a partial Undo.

### What this does not do

It does not bring deleted authors or narrators back. Recreating them means
deciding whether they get their old ID, and that needs the owner's decision.
The records are kept, so a restore can be built on them later.
