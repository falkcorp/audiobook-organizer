<!-- file: docs/executive-summaries/2026-10-30-author-reclassify-undo-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5de4f9c6-ba69-4f25-99e2-3c6c8b56ba68 -->
<!-- last-edited: 2026-10-30 -->

# Turning an author into a narrator can now be undone

## Executive Summary

The Authors page has a button that says an author is really a narrator. Pressing it
removed the author from the library and moved all of that author's books over to a
narrator of the same name. There was no way back: if it was the wrong call, the author,
its alternate names and the credit on every book had to be rebuilt by hand. The same
change made by the AI suggestions was already undoable; the button was not. It is now
undoable in the same way. That way is the server's undo request for the job, which is also
how a merge is undone today; the Activity page does not yet show an Undo button for author
jobs of either kind.

## What changed

- **The button starts a tracked job.** It shows up in the notification bell and in the
  Activity history like a merge does, instead of silently doing the work inside the click.
- **The job writes down what it changes first.** Each book's old credits and the removed
  author, with its alternate names, are recorded before they are touched, so undoing
  that job brings the author and every credit back exactly as they were.
- **It stands the library scan aside.** A scan running at the same moment rewrites the
  same credits, so the job asks the scan to wait. If the scan will not wait, nothing is
  changed. If the job loses that hold part way, it stops and keeps the author, rather than
  deleting an author that still has books on it.
- **The page waits for the job.** The page does not continue to a merge, or report
  success, until the job has finished, and a job that fails is counted as a failed
  reclassification with the same wording as before.

## What did not change

Who may press the button, what it does to the books, and how the page looks are the same.
One related item, where a stored AI suggestion whose kept author has disappeared is
re-checked every time it is read, is still open in the to-do list.
