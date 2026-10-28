### Fixed

- Two deliberately skipped backup-containment tests (`internal/fileops/backup_containment_test.go`, TASK-083) quoted their old failure output verbatim in the skip message, including `--- FAIL:` lines. CI's log scanner annotated those quoted lines as test failures on every full-suite run. The skip messages now paraphrase the measured output without the failure markers.
