### Fixed

#### Repairing a multi-file audiobook no longer slows down with every track

The repair that reconnects a book to its audio files created those files one at a
time, and after each one it re-added the book's totals from scratch — which means
re-reading every file it had created so far. A book with 200 tracks did not do 200
units of work, it did about twenty thousand.

Production logs settled how much this mattered: of all the total-recalculations that
could be attributed to a specific cause, **92% came from this one loop**, by a wide
margin the largest single source in the system.

The files for one book are now written together, in one operation, and the totals are
re-added once at the end. The result is the same; the work no longer grows with the
square of the track count.

Two things worth knowing about the new bulk-create:

- **It creates; it does not update.** It deliberately does not look for an existing
  file to modify, unlike the bulk-*upsert* used elsewhere. The repair already checks
  whether the book has any files before it starts, so there is nothing to match. Using
  it where an update was meant would add duplicate rows rather than change existing
  ones, which is why that behaviour is now written down as a test rather than left to a
  comment.
- **It refuses a batch containing two files with the same iTunes identifier.** That
  identifier is supposed to name exactly one file. The existing per-file check consults
  what is already saved, so it cannot see another file being written in the same
  operation — the two would both pass and both be written. It now stops instead.

**One caller was deliberately not converted.** The directory-probe repair creates files
one at a time by the same pattern and still does. It accounts for a small share of the
measured volume, and it decides per file whether to create one at all, so converting it
is a real change in shape rather than a mechanical swap. It is left as it is rather than
changed without the same evidence.
