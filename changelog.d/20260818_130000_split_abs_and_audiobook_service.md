### Changed

- Split `abs.Store` (10 methods) into `ABSUserStore` / `ABSSessionReader` /
  `ABSSessionWriter`, and `audiobooks.AudiobookService` (13 methods) into
  `AudiobookReader` / `AudiobookTrashService` / `AudiobookUserTagService` /
  `AudiobookViewDecorator`. Both original names are retained as the composition
  of their pieces, so the method sets are unchanged and no consumer moves.
