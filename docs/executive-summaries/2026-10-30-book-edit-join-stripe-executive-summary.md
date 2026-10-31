<!-- file: docs/executive-summaries/2026-10-30-book-edit-join-stripe-executive-summary.md -->
<!-- version: 1.0.1 -->
<!-- guid: 1c7f3b82-95ad-4e60-b2d4-0a8e61f7c539 -->
<!-- last-edited: 2026-10-30 -->

# Editing a book can no longer lose or scramble its author and narrator credits

**What was wrong.** When you saved a change to a book, the app first saved the
book itself and then, a moment later, saved the list of authors and the list of
narrators as separate steps. In that gap, anything else working on the same book
(a metadata update running in the background, or the narrator tidy-up) could
write its own version of the credits, and one side would silently overwrite the
other. The result could be a book whose narrator field named one person while
its narrator list named someone else, or credits quietly reverted to an older
state. Nothing reported an error.

**What changed.** The book and both credit lists are now saved together as a
single step while the book is held, so the edit itself can no longer leave the
book and its credits disagreeing. If the credits cannot be saved, or a narrator
cannot be looked up or created, the whole edit fails instead of leaving a
half-saved book with a warning. Some other background jobs still save a book
and its credits in separate steps; moving them over is tracked as a follow-up. A save that does not touch the credits (for example a title
change) no longer rewrites them, so the order of authors stays exactly as it was.

**Why it mattered.** Credits decide where a book is filed and who is shown as its
author and narrator. Losing or mixing them was invisible until a book turned up
in the wrong place or under the wrong name.

**One thing to know.** If an edit names a narrator who is new to the library and
the save then fails, that narrator is still created and sits unused until the
routine cleanup of empty narrators removes it.
