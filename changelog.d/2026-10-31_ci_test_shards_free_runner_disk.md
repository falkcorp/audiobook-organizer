### Fixed

- CI: the short-test and fixture-test shards remove the runner's unused .NET, Android, GHC and CodeQL toolchains before building, so a race build no longer fails with "no space left on device" from the linker (seen on shard 2/4 of #4330 and #4331).
