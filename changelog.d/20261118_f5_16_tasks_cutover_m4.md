### Added

- The kind-rename map of the R2 tasks cutover (`internal/books/cutover/kindrename`): HEAD's 269 stored operation IDs and its scheduler task names mapped to the task kinds the native engine will register, generated from the op-ID ledger, a checked-in dump of the booted registry's alias table, the retired-ID list and the scheduler's task table. A test fails when the generated file differs from a fresh run. Nothing reads it yet; the tasks flip registers the step that applies it. No production behaviour changes.
