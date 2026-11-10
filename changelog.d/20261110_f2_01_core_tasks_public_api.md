### Added

#### Task public API and `taskstest` kit, dark: `core/tasks`

New package `core/tasks` (`github.com/falkcorp/mediacore/tasks`) declares the platform-core task API of 05-tasks §5T.3.1–§5T.3.13: the `Task`, `Batch` and `Fixer` constructors over `Common`, `Kind`, `Effects` (built on `entity.Ref`), `Parallelism` (`Sequential`, `CPU`, `Fixed`, `FromParam`), `CancelContract`, `ResumePolicy`, `Permissions` and `Approval`, `Schedule` and `ScheduleConfig` (on `config.Duration`), the `RC`/`ItemRC` run context, `Writer`, `Source`, `Row`, `RunState`, the `Client`/`Executor` surfaces and the store records. `RunItems`, `RunSeq` and `ForEach` implement the item loop: a zero `Parallelism` fails before the first item with `ErrNoParallelism`, cancellation stops dispatch, sequential and parallel runs return the same `*ItemsError` shape, a broken pause gate, ref lock or CPU slot fails the item instead of skipping it silently, a panic becomes an item error with its stack, and a CPU loop nested in a CPU item runs on that item's slot instead of deadlocking. `BatchSpec.OnItemError` is a `BatchErrorPolicy` whose zero value, `BatchContinue`, keeps a Batch going past a failed item; `BatchStop` must be stated. Constructors collect problems instead of panicking; `Problems(d)` returns them for registration. `core/tasks/taskstest` adds `FakeRun` and the static half of `Conformance`. Nothing imports `core/tasks` yet, so no route, schedule or stored row changes.

### Changed

#### Spec Go-block harness waits on missing core identifiers, not only missing packages

`TestSpecGoBlocksCompile` (`core/examples/hello`) now pins each missing core identifier a 06 spec block waits for (`config.Handle`, `ui.Bundle`, the `module.Manifest`/`module.Registrar` members and `module.Setting*`), so the hello blocks keep waiting once `core/tasks` exists. Only existing core packages count, so a name missing from another spec block is still a compile error. A missing identifier never hides an independent compile error; an error downstream of a missing type surfaces in the row that lands the type. Every control pins its exact waiting set.
