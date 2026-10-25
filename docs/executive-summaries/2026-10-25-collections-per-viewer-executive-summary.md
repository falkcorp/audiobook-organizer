<!-- file: docs/executive-summaries/2026-10-25-collections-per-viewer-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5b8e2d71-3c4a-4f06-9a17-d2e6c0b94f83 -->
<!-- last-edited: 2026-10-25 -->

# Smart collections show each person their own listening

Pull request: https://github.com/falkcorp/audiobook-organizer/pull/4155 (merge commit added at merge).

## Executive Summary

- A smart collection is a saved search, such as "books I have finished" or
  "books I am less than halfway through". Until now, a search about listening
  was answered using the listening history of **whoever created the collection**,
  and everyone else was shown that person's answer.
- That leaked one person's habits to every other user and gave other users a
  list that had nothing to do with their own library activity.
- Now the answer is worked out for **the person looking at it**, every time they
  look. Two people opening the same collection can see different books. This
  also applies in the Audiobookshelf phone app.
- Administrators get no special treatment: an admin sees their own listening, not
  anyone else's.
- When the system has to work out such a collection with nobody in particular
  watching, the answer is "nothing". It never quietly falls back to the creator.
- Collections that do not mention listening are unchanged.
- If a collection's members cannot be worked out for someone (the search index is
  still starting, the saved search no longer makes sense, or their listening history
  could not be read), the page now says so instead of showing an empty list that looks
  like "nothing matches".

## What was wrong

**What it was.** Smart collections are shared across the whole server, but the
listening terms (finished, unstarted, percent listened, last played) belong to
one person. The earlier design, chosen on 18 October, resolved that by always
using the creator's listening, and by keeping a stored copy of the answer.

**Why it mattered.** The stored answer let any viewer infer what the creator had
finished or started, and the phone app served that stored copy directly. It also
meant a collection called "unfinished" was wrong for everyone except its creator.
A second flaw made it worse: if no person was identified, the system treated the
missing person as someone who had listened to nothing, so "unstarted" matched the
entire library.

**The fix.** The listening terms are now evaluated against the requester on every
read, in the website and in the phone app. No stored copy is kept for these
collections, because the right answer changes every time anyone listens, and
keeping one copy per person current would cost far more than working it out when
asked (a library has tens of collections, not thousands). Existing stored copies
are cleared once when the server starts. If no person is identified, the answer
is empty. Owner-takeover and "blocked collection" machinery, which only existed
to protect the old design, was removed.

## What this does not undo

Clearing the stored copies at startup removes them from the live database, but not
from every place a copy of the database already exists. Older database files that have
not been compacted yet, ZFS snapshots of the data directory, and backups can still
hold a creator's old stored list until they are compacted or expire. Those are
administrator-only places (they need access to the server itself, not the website or
the phone app), so this is a note for whoever manages the server's snapshots and
backups, not a user-facing exposure. Purging snapshots early is the owner's call.

## What users will notice

- A collection that filters on listening may show different books to different
  people. Its member count on the collections page is the viewer's own.
- The "Take ownership when saving" checkbox and the "not re-evaluated" banner are
  gone from the collection page, and the hint under the query box now says the
  fields use the viewer's own listening.
- The hourly background refresh no longer touches these collections.

## Verification

Automated tests cover two users with different listening seeing different
members, the creator's listening never deciding someone else's list, an
evaluation with nobody identified matching nothing, and the phone-app endpoints.
Deliberately switching the code back to the creator's listening made those tests
fail, which shows they would catch a regression.
