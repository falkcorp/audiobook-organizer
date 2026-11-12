### Fixed

- `TestRequestReset_IsWrittenBeforeTheNextEntry` no longer flakes: it issued its two coalescing `RequestReset` calls while the stream loop was live, so the loop could write the first reset between them and the second correctly owed a second frame. Both calls now happen before `Serve` starts. Product behaviour is unchanged.
