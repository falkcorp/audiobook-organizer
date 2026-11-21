### Changed

- A short decision table now records what happens to each HEAD operation-definition field the native kinds do not carry (`FormerIDs`, `Liveness`, `Cancellable`, `Plugin` and the dropped cron schedules): drop, or move to the kind-rename map or the schedule declarations. No behaviour changes.
