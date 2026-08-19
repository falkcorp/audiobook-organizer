### Changed

- `dedup/dataset.StoreAdapter` holds a two-method `AdapterSource` instead of a
  full `database.Store`. Its comment had claimed it needed the full interface;
  the adapter exists because of a method-name mismatch (`GetBookByID` vs
  `GetBook`), which has nothing to do with width.
