### Changed

#### Seven server-side constructor wrappers stopped re-declaring `database.Store`

`internal/server/audiobooks_compat.go` exists so the server package can keep using
the pre-move names of seven services. Each wrapper re-declared its parameter as
`database.Store` — 398 methods — and then forwarded the value into a constructor
that asks for far less. Four of the seven targets already declared a narrow
interface, so the wrapper was actively widening what callers had to satisfy.

They are now function-value aliases (`var NewRevertService = audiobookspkg.NewRevertService`),
the pattern the same file already used for `applyOverrideToPayload`. An alias takes
its signature from the target, so the narrow parameter propagates for free — and
when the three targets that still take `database.Store` are narrowed, these seven
follow with no second edit. `database` is no longer imported by the file at all.

Also narrows `revertServiceStore` from three wholesale `database.*` embeds to the
five methods a compiler probe measured: four direct calls plus `GetAllImportPaths`.

No behaviour change: calling a function-valued variable is identical at every call
site, and narrowing a parameter cannot break a caller.
