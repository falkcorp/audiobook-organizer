### Fixed

- The core health check-loop lifecycle test no longer counts the whole process's goroutines to prove that Register starts no loop. Another test's leftover goroutine moved that count and failed the test in unrelated merge-group runs. It now checks that the registry holds no loop context and that a registered probe is not called again over ten intervals of a fake clock before StartChecks.
