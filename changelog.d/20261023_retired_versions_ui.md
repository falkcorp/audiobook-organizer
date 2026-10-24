Added the retired-versions screens, the last of the stacked changes for retired
book versions. On a book's Files and History tab a collapsed "Retired versions"
panel lists the versions of trashed or merged books that were moved onto it,
with each version's source book, date, file count, size and duration, a list of
its files, and a Restore button that moves the files back to the trashed book
they came from. A "Show retired files" switch next to the book's files lists
its retired rows read-only below the live ones, so they can never be selected,
split or moved. Two server routes back this: `GET /audiobooks/:id/retired-versions`
and `POST /audiobooks/:id/retired-versions/:vid/restore`. The restore is a
first version: it is not journaled, it brings each file back without its earlier
version link, and it refuses (409) when the original book was purged or is no
longer in the trash. The file listing already accepted `include_retired=true`;
the web client now sends it from the switch.
