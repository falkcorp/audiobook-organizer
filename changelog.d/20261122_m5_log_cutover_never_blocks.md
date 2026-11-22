### Changed

- The system-log step of the storage cutover (`books.log-cutover`, not registered yet) no longer stops a boot. A system log row that the new journal refuses is moved to the quarantine with its raw bytes and the step goes on to the next row, and the step's check reports a row left behind or a count that does not balance as a warning instead of failing. Errors that mean the database itself is unhealthy (a failed quarantine, delete or batch commit, a cancelled boot) still stop the boot. A lost system log is acceptable; a stopped upgrade over it is not.
