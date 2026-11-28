### Changed

- A user merge can now stage up to 128 MiB in one batch (was 64 MiB). The limit was a guess; it is now sized at more than four times the largest account measured on a production snapshot (about 25 MiB, almost all saved preferences), so merging a heavy account is not refused as too large.
