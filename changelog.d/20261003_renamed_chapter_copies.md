### Fixed

- **A folder that holds each chapter twice, once renamed, is now one book.**
  Two files at the same chapter position with the same size are one chapter
  (owner decision 2026-10-03). The repair keeps one, retires the other's book
  into the merged book, and leaves that copy's file and file row in place;
  nothing is deleted. Before, the originals and the renamed copies were read as
  two works side by side and could become two books with the same audio. A
  same-position pair whose sizes differ is still held.
- **Dates and trailing "(n of m)" are no longer mistaken for disc-track
  numbers.** "01-05-1945" is not chapter numbering; "001 - Arrival (1 of 8)"
  sets merge again; a folder with "1-0x Book A" and "2-0x Book B" splits into
  one row per title; an em dash counts as a separator.
