### Changed

- The acoustid (8), metafetch (3), iTunes (1) and Deluge (4) maintenance operations now have native task kinds (`books.acoustid.*`, `books.metafetch.*`, `books.itunes.position-sync`, `books.deluge.*`) that run the same code as the existing definitions. Nothing registers them yet, so no behaviour changes today. The metadata-miss marker rows that the ASIN backfill writes are now a resource of their own (`asin_misses`), so the dispatcher can see that write.
