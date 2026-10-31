### Fixed

- Retiring, un-retiring or re-homing a book file now rewrites the owning book id stored in the file's near-duplicate fingerprint index rows (`fpidx:`) in the same batch that re-keys the row, so the index never names the shell a retired row left (or the holder a restored row left). Previously it kept naming the old book until the index was rebuilt. The index build op's write (`PutLSHEntries`) now takes the owner from the file's row under the file's write stripe instead of trusting the book id its caller read earlier.
