### Fixed

- Folder-sized books repair now finds author folders of whole books stored flat (three or more files of 3 h+ under distinct names) and folder-books whose files span two shelves. A two-shelf row is applicable only when 80% of its files share a shelf and every other file has a proper book; otherwise it is listed as `skipped_mixed_shelf` instead of being silently ignored. Found via an author-folder row of 34 one-file books that hid a book from search.
