### Added

#### `diagnostics user-state`: count accounts and books with listening state, read-only

`audiobook-organizer --db <store> diagnostics user-state [--books <file>]` runs with the service stopped. It opens the store read-only and prints one `USERSTATE={...}` line with the accounts in the store's account list, the system accounts, and the books with listening state. The book counts are made through the same checks that merges and deletes use before they remove a book. It refuses a store the identity cutover has not run on, and exits non-zero on any error. A count it could not read is printed as null, never as 0, and no account or book id appears in its output. The `--books` file (mode 0600) adds a count for a sample of book ids.
