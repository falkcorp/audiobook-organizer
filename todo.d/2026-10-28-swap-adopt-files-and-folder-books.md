- [ ] **TODO-SWAP-ADOPT-FILES** Let `versions.RunVersionSwap` take a version whose files are
      not in its `.versions/{id}/` slot, and support folder (multi-file) books. The swap now
      refuses (409 `NO_SWAP_TARGET`) unless the entering version's files are already in its
      slot and the book's path is a single file in the folder the slots belong to, and nothing
      in production puts alt files in their slot, so trashing the active version of most books
      is refused until the swap can adopt files from elsewhere (a download folder, another
      folder of the book) safely: copy or move them into the slot first under the same
      checks (never the same file as the active version's, never a symlink, same filesystem or
      a verified copy), then swap. The same task covers a book whose `file_path` is a folder
      (inplace multi-file): the swap must move the whole folder's files, not assume the
      parent of `file_path` is the book's folder. Resuming a swap that did not finish
      (`ErrSwapIncomplete`, `ResumeVersionSwaps`) is tracked separately as
      TODO-SWAP-STUCK-RECOVERY.
