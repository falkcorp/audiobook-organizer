### Changed

- Groundwork for the tasks cutover, not yet reachable: the native stand-down hold can now be renewed by the run that holds it (`Hold.Renew`), and a lapsed lease stops the run's writes. The native folder-cover, chapter-folder repair, iTunes clone and missing-file repoint kinds wait while a library scan runs, and a continued transcribe-file-intros attempt queued behind a scan now waits before it starts instead of inside the run. Nothing changes for the running server.
