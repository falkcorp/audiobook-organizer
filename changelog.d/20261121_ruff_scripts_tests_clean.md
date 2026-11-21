### Fixed

- `ruff check scripts` is clean again: the fixture-shard-weights test's `T` helper is renamed `timing` and its lambda is a `def`, and the shard-parsing test asserts `argparse.ArgumentTypeError` instead of a blind `Exception`. Test-only.
