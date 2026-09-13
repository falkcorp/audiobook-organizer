### Fixed

- Books whose tags repeat a track number inside one folder (every file tagged track 1 is common) are renamed in natural file-name order within that folder ("Book 2" before "Book 10") instead of being refused. Previously the planner refused them, and with write-back on, the batch apply's rename check then refused their metadata too, so they could never get metadata through batch apply. A book is still refused when its file names also cannot order it: names that differ only by extension, case or leading zeros. Books whose tags already order their files are unchanged.
- When a renamed book has a missing file, that file keeps its own track number and the present files are numbered around it, so a file that comes back returns to its number.
