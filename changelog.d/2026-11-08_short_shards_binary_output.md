### Fixed

- CI: the short-test shard runner (`short_test_shards.py`) now decodes `go test` output with undecodable bytes replaced. A test that printed raw gzip bytes made strict UTF-8 decoding raise `UnicodeDecodeError`, which crashed the whole shard and threw away every real test result in it.
