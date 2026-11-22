### Added

- The books module can now build every task list it will register at the tasks flip from one entry point per package (F5-18 A6): maintenance (jobs, groups 2, 3, 4 and 7, aux-store compaction), metafetch, deluge, itunes, acoustid, the scheduler, and the Repairs fixers, all over narrow store interfaces and a writer that stamps each write with its row. Nothing registers them yet, so no behaviour changes: `bookTaskLists` is not called from `Module()`, and a registration test pins which scheduled kinds are carried and which are still waiting on a builder.
