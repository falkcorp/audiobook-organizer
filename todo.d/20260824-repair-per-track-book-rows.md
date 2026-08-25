## Repair the book rows that were written one-per-track

The multi-file detector could not read a trailing sequence number (`Name 001`),
so any folder named that way was imported as one book PER TRACK. Fixed for new
scans on 2026-08-24, but **preventing the corruption is not repairing it** — the
rows already written stay wrong, and nothing re-groups them.

Confirmed on the production library:

- `/mnt/bigdata/books/newbooks/audiobooks/Terry Pratchett Carpe Jugulum/` — 80
  files on disk, 80 book rows in the DB, titled `Pratchett 001`…`Pratchett 080`
- each row took the **folder name** as its author: `Terry Pratchett Carpe Jugulum`
- each row got its own `version_group_id` with `is_primary_version=false`

- [ ] **Measure the real size of the affected population first.** One folder is
      known. The query is books whose siblings share a directory and whose titles
      are file stems — do NOT assume it is only newbooks, and do not estimate it
      from the 80.

- [ ] **Decide the repair shape with the user before writing it.** Collapsing a
      per-track group means merging N rows into 1 and deleting N-1, re-deriving
      the title from the folder, re-resolving the author, and rebuilding one
      version group. That is destructive and it is not a drive-by.

- [ ] **Check whether the author is correct once grouping works.** The folder-name
      author (`Terry Pratchett Carpe Jugulum`) is downstream of the grouping
      failure and is expected to improve when the folder becomes one book, but
      that has NOT been verified — do not assume the grouping fix closed it.
