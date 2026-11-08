### Fixed

#### Core's journaled swap: tests now pin the twin proof and the mode order

The restore swap's twin proof now has tests for a symlink at either name, which keeps the `Lstat`, not `Stat`, of the final component that stops mv deleting the only copy. On macOS a test covers a directory reached through a firmlink, which only the directory check refuses. The tests for a filesystem with no modes now count writes, so a state file can never receive data before it is made 0600.
