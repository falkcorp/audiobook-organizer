Fixed the second re-review findings on the retired-version readers. When a
combine plans a single-file book's file and several books already hold a row at
that path, it now takes the row of a book in the same combine (the bulk move
brings it), then the row of a trashed book, then the row of the one live book
holding it. With two or more live holders, or one whose book no longer exists, it
plans nothing and writes a warning on the combine's undo journal instead of
taking a live book's only file row; before, it took the oldest book's row without
saying so. A combine whose rowless primary book has the same file path as a row of
a book it absorbs no longer stops after writing its journal with "file not found":
the survivor's own file attach used to move that row before the bulk move looked
for it, which failed every such combine. Two merge tests that combined a book
before the in-memory index finished warming up failed on a loaded runner; the test
store now waits for the warmup, so no test starts without it.
