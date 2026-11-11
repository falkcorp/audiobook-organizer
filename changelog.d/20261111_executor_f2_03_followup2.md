### Fixed

- core/executor: a task start can no longer claim the core-only system priority for a module's kind, or name a priority past it. Before, such a run took the workers reserved for core plumbing, or jumped ahead of system runs in the queue. Start, StartIn and child starts refuse both, and a module kind may not default to the system priority (F2-03 follow-up 2).
- core/executor: starting a task while an identical run of it is already running now joins that run, as the task spec and the previous engine do. Before, the default policy never joined an existing run and no policy joined a running one. Kinds that merge or key their starts still join only queued runs (F2-03 follow-up 2).
