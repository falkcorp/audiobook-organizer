### Fixed

- **`TestEncryptionHelpersAndSettings` no longer fails depending on test
  order.** It asserts that `EncryptValue` errors while the package-global
  settings encryption key is unset, but only saved and restored the key
  instead of clearing it. Three other tests in `internal/database`
  (`TestInitEncryptionWithExistingKey`, `TestGetDecryptedSettingSecret`,
  `TestDecryptValueErrors`) called `InitEncryption` and left their key
  installed, so the assertion failed whenever one of them ran first: on
  every `-count>=2` run of the whole package (the second iteration runs
  after the first's leakers) and on most `-shuffle` seeds. All four tests
  now use the existing `withCleanKey` helper, which clears the key and
  restores the previous one on cleanup. Test-only change.
