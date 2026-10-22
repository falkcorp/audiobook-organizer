`DELETE /audiobooks/:id?block_hash=true` no longer blocks a file hash that a live
book still holds. The delete proceeds, the hash is left unblocked, and the
response carries `warnings` naming the live books (a blocked hash makes the
scanner skip that book's own file; prod had one blocked hash shared by two live
books). Holders are found through the `book:hash:` index plus the multi-valued
book_file hash indexes, never a library scan, and a lookup that cannot be
completed also skips the block and says so. The web delete dialogs no longer
default "block hash" to on. Known gap: two books whose only hash record is
`Book.FileHash` with no book_file row are not both visible to the index; only
its owner is. Separately, the hard delete of a book removes the `book:hash:`
row even when another live book is the one that row names (not changed here).
