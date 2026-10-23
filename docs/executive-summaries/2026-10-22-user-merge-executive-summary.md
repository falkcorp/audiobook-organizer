<!-- file: docs/executive-summaries/2026-10-22-user-merge-executive-summary.md -->
<!-- version: 1.0.2 -->
<!-- guid: e2a85c31-7d94-4b06-8f1c-6a30d9e4b752 -->
<!-- last-edited: 2026-10-23 -->

# Merging two accounts into one

## Executive Summary

Some people ended up with two accounts: one they sign in to the website with,
and another that came in through single sign-on or an old import. Their
listening progress, bookmarks, saved sign-ins and API keys were split between
the two. There is now an admin tool that merges one account into the other, and
it can be undone. Nothing has been merged yet; the owner chooses which account
stays and when to run it.

## What the tool does

- **Look first.** A "Sign-ins & data" panel on the Users page shows what an
  account holds: its linked sign-ins (partly hidden) and how many books,
  positions, bookmarks and keys it has.
- **Preview before anything is written.** "Merge into..." shows exactly what
  would move, where both accounts have progress on the same book and which one
  wins, and anything that would stop the merge. It also says which account holds
  the owner's sign-in. If that is the account being merged away, or the tool
  cannot tell (no owner address is set, or a sign-in has no address to compare),
  the admin has to tick a box saying they understand it moves. If the account
  that goes away is the only one that had verified an email address, the
  account that stays keeps the address as verified; the preview says so, and
  warns when an address will be left with no verified holder.
- **Only what was previewed runs.** The confirm button carries a fingerprint of
  the preview. If either account changed in between, the merge does not run and
  the new preview is shown instead.
- **The account that goes away is locked, not deleted.** Its sign-ins, keys and
  listening data move to the account that stays. It cannot be switched back on by
  hand; undoing the merge is the way back.
- **Undo.** Every change is recorded with what it replaced. Undoing puts things
  back, but a book either account listened to after the merge is left exactly as
  it is and reported (its progress, its status and its positions are never
  split), and anything else changed since is left alone and reported, never
  overwritten. A sign-in or API key goes back to the account it came from with
  only its owner changed, so uses it gathered since are kept. When sign-ins or
  keys move back, the account that stayed is signed out of the website and
  Audiobookshelf apps.
- **Safe to interrupt.** If the server stops part way, the merge can be resumed
  from where it stopped. Resuming plans the work that is left again against the
  accounts as they are, so a change made while it was stopped does not block it.

## Who can use it

Only a signed-in admin, from the website. API keys and Audiobookshelf tokens are
refused, and when an owner is configured the owner gate applies too. The server
will not start a merge from anywhere else, even through the general operations
page. The record of a merge on the operations page shows only counts and account
ids, never names, email addresses or the rows involved; those appear only on the
admin Users page.

## Related fix

Older "undo" records for merged books, and for the Audible read-status import,
name the account as it was when they were written. They now follow an account
that was merged to the account that holds its data, so those undos keep
working after an account merge.

## What was checked, and what was not

Checked: the merge, resume and undo paths against test data, including
simulated crashes; that undo leaves later changes alone; the sign-in and
credential-route protections; the preview and confirm dialog.

Not done yet: the merge has not been run against the real library. Before the
owner runs it, open "Sign-ins & data" for both accounts, read the preview, and
check the owner-sign-in line.
