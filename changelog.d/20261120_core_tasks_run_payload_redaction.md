### Security

#### core/tasks: the run topics redact every field the REST routes redact

`RedactRunPayload` passed only `Current` through `RedactRunView`. Any other
field the rule blanked (error text, for example) would have been blanked on
`GET /runs[/{id}]` and still sent on `core.task.run-changed.v1` and
`core.task.finished.v1`. It now turns the payload back into the `RunView`, runs
the whole view through `RedactRunView` and re-encodes it, the same path the REST
routes take. A mode word the encoder never writes leaves as `unset`. The
REST-versus-stream test now sets every field, with a guard that fails when a new
field is left unset. The run payload's wire keys are pinned as literals in both
the topic test and the REST test. Dark: the run topics have no publisher yet
(F5-18).

### Fixed

#### core/tasks: stale comments and an undeclared spec type

The `RunView` comment and a topic test comment now name `RunPayload` as the
run topics' payload. A `visibleToAdmins` comment named `core:admin` where it
meant `core:system.manage`. The SPA's `coreEvents` types name
`tasks.RunPayload`. The 05-tasks spec block declares `ActorPayload` and
`NewActorPayload`.
