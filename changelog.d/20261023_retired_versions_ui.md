Added the retired-versions screens, the last of the stacked changes for retired
book versions. On a book's Files and History tab a collapsed "Retired versions"
panel lists the versions of trashed or merged books that were moved onto it,
with each version's source book, date, file count, size and duration and a list
of its files in disc and track order. Each version has a Restore button that
is disabled for now, with the reason as its tooltip: "Restore will be available
once restores are journaled", or that the original book was purged or is no
longer in the trash. The button is not shown to a caller without the
library-organize permission. A restore that is not journaled would drop each
file's earlier version link and could not be undone, so the restore route
`POST /audiobooks/:id/retired-versions/:vid/restore` answers 501 Not
Implemented and changes nothing until journaled restores exist. A "Show retired
files" switch next to the book's files lists its retired rows read-only below
the live ones, so they can never be selected, split or moved. The listing route
is `GET /audiobooks/:id/retired-versions`; each version now reports
`shell_in_trash` as well as `shell_exists`. The file listing already accepted
`include_retired=true`; the web client now sends it from the switch.
