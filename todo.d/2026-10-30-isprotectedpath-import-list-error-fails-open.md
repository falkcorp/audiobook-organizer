- [ ] **TODO-ISPROTECTED-IMPORT-ERR-OPEN** `Server.isProtectedPath`
      (`internal/server/server_middleware.go`, the import-path block) drops the
      import paths when the cached read of the list fails (`err == nil` guards the
      loop and nothing else happens on an error), so the static check runs without
      them. The organizer's rename-source guard is built on it
      (`renameSourceVerdict`), so while the list cannot be read a file under an
      import path is answered `SourceMovable` and an in-place move or `RenameFiles`
      can take it out of the import folder: fail-open, where the audiobooks helper
      (`protectedPathChecked`) reports the failed read and the purge keeps the file.
      Make the server's check fail closed on a failed list read (treat it as
      protected, or return the error to the caller the way `protectedPathChecked`
      does) and log it once; add a test with a store whose `GetAllImportPaths`
      fails and a file under a configured import path. Found in the review of #4258.
