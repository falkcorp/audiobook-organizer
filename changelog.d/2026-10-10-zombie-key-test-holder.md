### Fixed

- `TestZombie_HoldsKeyUntilGoroutineExits` no longer flakes (it failed about 5 runs in 100). It checked that no concurrency key was held after the zombie exited, but it could not tell the zombie's key apart from the second op's fresh claim on the same key. `HeldConcurrencyKeysForTest` now reports who holds each key, and the test asserts the first op holds none.
