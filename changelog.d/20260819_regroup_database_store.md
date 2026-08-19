### Changed

- `database.Store`'s 40 directly-embedded sub-interfaces are grouped into six
  domain composites — `CatalogStore`, `MediaStore`, `AccountStore`,
  `EnrichmentStore`, `OperationsStore`, `PlatformStore`. This is a regrouping,
  not a narrowing: the method set is byte-identical (`verify_interface_split.py`
  reports `Store 40 -> 40 IDENTICAL`) and no consumer moved. It clears the last
  `interfacebloat` finding in the repository, so the interface-width baseline
  drops from 1 to **0** and any new over-wide interface now fails CI outright.
