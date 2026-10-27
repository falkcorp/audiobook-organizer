<!-- file: docs/executive-summaries/2026-10-24-repairs-visibility-followup-executive-summary.md -->
<!-- version: 1.5.0 -->
<!-- guid: 5b9d3e71-c4a2-4f08-8e16-2a7f0d6c9b34 -->
<!-- last-edited: 2026-10-26 -->

# Repairs runs and their activity are private to the people who may use them

## Executive Summary

The Repairs page runs fixers that change the library on someone's behalf, and
some of them handle other people's data, such as their listening progress. An
earlier fix stopped people from reading a fixer's plan, results and change
history unless they were allowed to use that fixer. Other places still showed
the same runs: the newer operations screens, the Activity log, the dashboard,
and the generic "start an operation" routes, the system status screen, and the undo
button. This change closes those, and makes the Audible reading-status fixer
need the permission to manage users (which administrators hold).

## What changed

- The Audible reading-status fixer now needs the permission to manage users, the
  same as the master-list import, because it writes any user's reading state.
  Editors can no longer plan, apply or read it.
- The newer operations screens leave out a Repairs run from anyone who may not
  use its fixer: the run lists, the counts (so a number never hints that a hidden
  run exists), the timeline, the live feed and the per-run pages. Cancelling such
  a run is refused to anyone but the person who started it. Approving it is
  refused to everyone who may not use the fixer, the starter included.
- The generic start and retry routes now ask for the same permissions as the
  Repairs page. Before, someone allowed to change settings could start the
  Audible reading-status fixer through them without being allowed to manage
  users.
- The Activity log leaves out the log lines of those runs, and its totals and
  pages are counted without them, so paging never shows a gap. The nightly
  summary entries have the hidden runs removed from their list of items.
- The dashboard no longer names those runs in its recent operations, and no
  longer hands out any operation's result data.
- Undoing an operation now asks for the same permissions as reading it, so a
  person who may not read a fixer's history can no longer undo it, and the report
  an undo returns no longer lists notes that name other users to anyone who may
  not manage users (it says how many were withheld).
- Activity log lines that name another user are kept from everyone who may not
  manage users, including lines that were grouped into a batch.
- The system status screen (settings permission) now leaves out the same runs
  and no longer hands out any operation's result data.
- The change-journal writer used by fixers now refuses to record a kind of
  change that nobody has classified as either one user's data or library data,
  so a new kind cannot become readable by everyone by default.
- A run record that is damaged no longer breaks the whole list of runs; it is
  skipped and logged.
- A review of this change found more places that still showed other people's
  data, and they are closed too. Account audit entries (an email address taken
  over by another account, a user merge, guest access tokens) and log lines that
  name a user only by account id are now kept from everyone who may not manage
  users. The results page of the user-merge and merged-user-repair operations,
  the diagnostics download, every operation log screen and download, and the
  live feeds follow the same rule.
- Undoing a change that carries one user's data now needs the permission to
  manage users, the same as reading it. Editors who could undo such an
  operation before can no longer do so; undoing library-only operations is
  unchanged.
- Both live feeds re-check a person's permissions for every message, so lowering
  someone's role or disabling their account takes effect on the next message
  instead of when they reconnect.
- Reading the Activity log for one operation or one book no longer builds the
  whole history of that operation in memory before choosing a page, which any
  viewer could previously make the server do over and over.
- Everything else, including every other kind of operation, looks the same as
  before to a person who only has view access.

## A second security review, and what it changed

After the first round of fixes a second review went through the same work and
found one serious problem and a handful of smaller ones. All of them are fixed
in this change.

- The list of past answers the matcher has accepted or rejected showed who gave
  each answer. It now shows that only to a person who may manage users; everyone
  else still sees what was accepted or rejected on which book.
- The download of a diagnostics export is now allowed only to a person who could
  have read everything inside it. The export remembers what the person who built
  it was allowed to see, and the download checks that against the person asking.
  Before, a narrower account could download an administrator's export.
- Starting or retrying an operation through the general routes now records who
  did it, so an export started that way is trimmed to that person's access
  instead of being built with no limit.
- A one-time permission an owner grants for a Repairs apply can now only be used
  by the operation the owner's own approval created. It used to be possible to
  use it from a different start made by the same signed-in owner.
- The check for "does this log line mention a person" learned the shapes that
  carry only an account number, and the same list now also cleans the extra
  details recorded with an activity entry.
- A live feed that has been open for hours no longer asks the database about its
  person for every message; it asks at most every five seconds. The price is that
  a role lowered in the last five seconds can still be served for up to that
  long, never longer, and never wider. A feed also closes by itself when the
  account is switched off or the sign-in or key it was opened with is cancelled.
- Reading the history of one operation or one book is now limited both in how
  many entries it will examine and in how far down the list a request may page.
  A second review found that the first limit could be defeated by asking for a
  very deep page, so every path now refuses deep pages and caps the page size.
- The general System Logs list now leaves out lines that mention a person for
  anyone who cannot manage users; the earlier notes said it did, and only one of
  its two branches did.
- An operation's error message and progress text can mention a person ("could not
  move state for user ..."). They are now replaced by a short notice for anyone
  who cannot manage users, on the operation pages, the live feed and the
  diagnostics export. The label of the item a run is on right now ("moving state
  for user ...") is held to the same rule, on the operation pages and the live
  feed.
- A third review found the same kind of text served raw by the Repairs lane's
  own list (the error of a fixer's last run), by the live worker labels of an
  operation, and by the reconcile and candidate-fetch pages. All of them now
  follow the same rule. Two status pages (the AI diagnostics results and the
  iTunes import status) also answered for any operation's number, so they could
  show the progress of a Repairs run their caller may not see; each now answers
  only for its own kind of operation.
- The result page of a diagnostics export no longer shows the server-side file
  location of the archive to anyone who cannot manage settings, the permission the
  download itself needs.
- The Undo button disappears when the server has already said the person may not
  undo that operation, instead of offering a button that can only fail.
- A new automatic check fails the build when a new kind of operation result
  contains a user's account number and is not placed behind the manage-users
  permission or the Repairs rules.

One decision worth recording: undoing a book "combine" or sibling move still
needs only the library-organize permission. That undo puts back the library's
structure (the absorbed book and the progress that was folded into the survivor),
which that permission governs. Requiring the manage-users permission would take
the combine-undo away from every editor whenever any user had progress on the
absorbed book. The undo report is still stripped of other people's rows.

## Gaps this change leaves open

- The nightly summary entries still carry overall counts that include the hidden
  runs (how many rows of each kind, and the tag totals), so a number can hint
  that such a run existed. Their items are removed.
- The Activity page's source counts are not filtered, and a few other overview
  lists (stale operations, the pause list) are not yet covered.
- Whether a log line names a user is decided by matching its text, so a line
  that names a user in an unusual way can still be shown.
- Some fixers record their changes without going through the checked writer, so
  the new refusal of unclassified change kinds does not reach them.
- Which notes in an undo report name a user is decided by a coarse word match,
  so a note that only uses the word "user" is withheld too.
- The System Logs page's general list still reads the old operation-log
  storage, which nothing writes to any more, so it is empty in production; it
  also does not apply the Repairs-run visibility rule to the operations it walks.
- A Repairs run recorded before this change with no fixer named stays hidden
  from everyone and cannot be cancelled by an administrator who did not start it.
- If the record of a Repairs run has been deleted, its Activity lines that name
  the Repairs lane are hidden from everyone, but the engine's own plain log lines
  for it cannot be told apart and stay visible.

These are tracked in the to-do fragment filed with this change.
