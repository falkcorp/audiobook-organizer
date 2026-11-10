### Fixed

#### Server start: a stop that begins before the task scheduler starts now keeps it from starting

The post-listen start now checks for a stop between registering the ops schedules and starting the task scheduler. `scheduler.Start` launches the run-on-startup tasks on goroutines nothing tracks, so a stop that began after the check before the scheduler was built would otherwise have had those tasks start during the stop.
