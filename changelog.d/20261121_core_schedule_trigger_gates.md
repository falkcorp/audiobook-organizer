### Added

#### Two trigger gates on core task schedules: `OnStartWhen` and `WindowWhen`

A core task schedule can now gate its triggers separately. `OnStartWhen(gate)` makes the start-of-process run follow a setting of its own: the run happens when that setting is on, even if the task's regular timer is switched off, and nothing happens when it is off. This keeps the "scan the library on startup" setting working the way it does today once the books module moves onto the core scheduler. `WindowWhen(gate)` takes a task out of the nightly maintenance window without stopping its regular timer, which is how today's per-task maintenance toggles behave for tasks that also run on an interval. The existing `When` gate still switches off every trigger of a task.

The window's opening now lists only the members its run will start, with each member's gates read at that moment. The list the window-start permission check uses still includes gated members, because a gate can open before the window run reaches that member. No task uses the new gates yet; the books schedules adopt them in a follow-up.
