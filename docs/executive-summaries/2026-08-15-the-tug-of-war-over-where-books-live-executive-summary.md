<!-- file: docs/executive-summaries/2026-08-15-the-tug-of-war-over-where-books-live-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2b7c4e19-8d05-4f36-a1c2-7e93b6a05d84 -->
<!-- last-edited: 2026-08-15 -->

# The tug-of-war over where books live

**2026-08-15 — why books kept moving, and why the fix means everything moves once**

## The complaint

Books would not stay put. A book organized into one place would later turn up
somewhere else, and organizing it again would move it back. Nothing looked
broken — every individual operation reported success — but the library never
settled.

## What was actually happening

The program had **three separate answers** to the question "where does this book
belong," and it never noticed they were different answers.

- **Organizing** a book used the folder and file naming patterns from Settings.
- **Saving metadata** used a *different* setting entirely, one that produced a
  path two folder levels shallower.
- **Organizing a multi-file book** — an audiobook split into one file per
  chapter — used the folder pattern for the folder, and then simply kept
  whatever the files were already called.

Moving a book is a real move on disk. So each of these dragged the book toward
its own answer, and the other two dragged it back. There was no arrangement that
satisfied all three at once. The tug-of-war could continue for as long as both
operations kept running, and every round was reported as a success, because from
each one's own point of view it was.

None of the three was simply "the correct one." Each had been fixed over time in
ways the others never received. The most consequential example: a fix that stops
a book's title from breaking a path apart — a real incident, where one title
containing a "/" split a single 85-chapter audiobook into 85 separate books —
had only ever been applied to the organize path. The path that renames books
after a metadata save had never had it.

## What changed

There is now **one** place that decides where a book goes, and all three
operations ask it. They cannot disagree, because there is no longer anything to
disagree with. A test now takes one book and checks all three routes produce the
identical answer, including the multi-file case — the one that nothing had ever
compared, and where the third disagreement had been hiding in plain sight.

Two other things surfaced while doing this:

- **Multi-file books were never named properly.** Only single-file books ever
  had the file naming pattern applied. Chapter files kept whatever names they
  arrived with. They are now named from the pattern and numbered by chapter.
- **Some database records pointed at files that were never created.** When
  organize made its copy of a multi-file book, it filled in each file's location
  by *assuming* the name stayed the same, without ever checking the disk. Those
  records now come from the same component that did the copying, and each one is
  checked against reality before it is saved.

## The one thing to expect

**Your files will move once.** Books that had settled at one of the three
answers will relocate to the single agreed one, and multi-file books will have
their chapter files renamed. This is a one-time cost, and it is the point: the
back-and-forth ends because there is finally one destination to end at.

Nothing is deleted and nothing is merged. A safeguard was added specifically for
this: if the file naming pattern does not distinguish one chapter from another —
which was true of the pattern actually in use — every chapter of a book would
have computed the *same* filename, and a forty-part audiobook would have
collapsed into a single file. The program now detects that and numbers the
chapters instead.

## What was deliberately left for later

Three related problems were found and confirmed by hand but **not** fixed here,
because each is a separate change and this one was already wide:

- Saving metadata reports success even when the file work fails — the result is
  discarded before anyone can see it.
- Copying a book into the library can produce an empty folder and a record
  pointing at it, if the original files have gone missing from disk.
- Deciding whether a file already at the destination is "the same file" still
  falls back to comparing file sizes rather than contents.

They are filed with their evidence rather than described as done.
