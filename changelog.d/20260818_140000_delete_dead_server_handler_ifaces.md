### Removed

- Deleted `internal/server/interfaces.go`. Its four interfaces
  (`bookHandlerStore`, `userHandlerStore`, `playlistHandlerStore`,
  `metadataHandlerStore`) had no references outside their own file — nothing
  took them as a parameter or field. The handlers they claimed to document now
  live in `internal/server/handlers/*` with their own enforced narrow
  interfaces.
