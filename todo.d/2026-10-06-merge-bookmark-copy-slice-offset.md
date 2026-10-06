- [ ] **Merge bookmark copy should honour SliceMapping offset.**
      `copyBookmarksForMerge` (`internal/merge/bookmark_copy.go`) copies a
      retired book's bookmarks onto the survivor at their raw times, even
      when the follow carries a `merge.SliceMapping` (the retired book is a
      slice of the survivor at `OffsetSeconds`). Progress already maps
      through the slice (`follow_journaled.go`), bookmarks do not, so a
      bookmark on a slice lands at the wrong place in the survivor. Found in
      PR #3791 review (consolidation-leftovers same-path twin). Map each
      bookmark by the offset when the slice is mappable, keep raw (or skip)
      when it is not, and keep the user-state "never lost" probe
      (`owedBookmarks`) agreeing with whatever the copy does.
