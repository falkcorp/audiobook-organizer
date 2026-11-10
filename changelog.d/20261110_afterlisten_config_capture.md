### Fixed

- The server's post-listen start no longer reads the process-global config on its own path. StartHook now captures every setting that path reads directly (auto-scan gate and debounce, the startup read-only key, the acoustid backfill gate, and the maintenance window the scheduler's Start reads) and passes them in, so a stop that abandons the start cannot race a later config edit. The scheduler's per-task enable and start closures still read the live config.
- A stop no longer withholds the HNSW clean-stop marker for a post-listen start that finished behind the stop's wait.
- A file-I/O submit dropped at a stop, or by a stopped pool, no longer leaves its in-memory pending entry behind (the persisted row stays for the next start's replay).
- The task scheduler is published through an atomic pointer, so the request-time providers and the maintenance-window op no longer race its assignment.
