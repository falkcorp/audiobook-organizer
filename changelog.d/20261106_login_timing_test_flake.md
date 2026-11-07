### Fixed

- Tests: the login timing tests no longer fail on a loaded CI agent. Every failed-login path (wrong password for a real account, unknown name, account without a password, disabled account) was already doing the same password work; the tests now prove that directly by counting the argon2id derivations each login spends (new read-only `absauth.DerivationsForTest`), which holds under `-short -race` at any load. The wall-clock comparisons stay as an end-to-end check, compare each path's fastest sample instead of a median of five, rotate the order each round, and skip under `-short`.
