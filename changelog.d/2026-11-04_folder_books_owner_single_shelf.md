### Changed

- The folder-books repair now offers the owner a single-shelf row too, when
  every reason it is held back is one the owner may resolve (a new book for
  files in the library folder, or a duplicate title that names exactly one live
  book). Until now only a row whose files sat on two shelves (mixed-shelf) was
  the owner's; a row on one shelf stayed listed and was only counted. The row
  stays skipped for a plain or scheduled apply, and a hard hold (an iTunes id,
  listening progress, no heir, a deep folder) still keeps it away from the
  owner. The owner-apply preview now names the row's real class (shelf,
  author-copy or mixed-shelf). The `single_shelf_owner_resolvable` row flag, its
  plan count and its Repairs chip label are removed, since the rows it measured
  are now offered directly.
