### Added

- The audiobook module's task-kind labels (`web/src/modules/books/layout/taskKinds.ts`), keyed by the kinds the task engine records runs under, with a test that reads the kind-rename table (`internal/books/cutover/kindrename`) and fails when a label's kind is not where that table sends its legacy operation id. Not used by the running app yet.
