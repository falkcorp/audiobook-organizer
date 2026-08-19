### Changed

#### `SchedulerDeps.Store` was `func() database.Store` — all 398 methods for seven

The scheduler received the whole store through a getter field. Probing it reports
seven calls: `GetSetting`, `SetSetting`, `CreateOperation`, `GetOperationByID`,
`UpdateOperationError`, `GetOperationV2`, `ListActiveOperationsV2`. The field is now
`func() SchedulerStore`.

Because Go requires function types to be **identical** for assignment, a
`func() database.Store` cannot be assigned to a `func() SchedulerStore` even though
`database.Store` satisfies `SchedulerStore`. The wiring sites therefore use a small
adapter closure rather than a bare method value. A nil `database.Store` converts to a
nil `SchedulerStore`, so the documented "may return nil before the DB is up" contract
is unchanged.

#### The last inline undo store parameter

`server.RunUndoOperation` took an inline anonymous interface embedding
`database.BookStore` + `BookVersionStore` + `OperationStore` — 90 methods — for the
five that `undo.RunUndoOperation` and the Deluge callback need between them.

With this, every interface in the codebase whose body was nothing but `database.*`
embeds has been narrowed, except `maintenance.JobStore`, which is settled by its own
arbitration.
