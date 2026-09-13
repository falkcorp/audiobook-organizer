### Changed

#### Collections — duplicate-name conflict is now a typed error (`ErrCollectionNameInUse`)

`PebbleStore.CreateCollection` and `UpdateCollection` now wrap a new
`database.ErrCollectionNameInUse` sentinel with `%w` when another collection
already holds the name. The native (`internal/server/handlers/collections.go`)
and Audiobookshelf (`internal/server/handlers/abs/collections.go`) collection
handlers detect it with `errors.Is` instead of matching the text
`"already in use"`, so rewording the message can no longer silently turn a 409
into a 500. The native create handler's extra `"duplicate"` substring match,
which no store path produced, is gone. The response is still 409 with the
message naming the collection.
