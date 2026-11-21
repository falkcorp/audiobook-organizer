### Added

#### A third trigger gate on core task schedules: `IntervalWhen`

A core task schedule can now switch off its regular timer on its own setting while staying in the nightly maintenance window and keeping its start-of-process run. The timer behaves exactly as if the schedule were switched off: nothing is recorded while the setting is off, and the next run comes when the setting is turned back on. The library scan needs this to keep today's behaviour once it moves to the core scheduler. Today its periodic scan has its own on/off switch, but the nightly window also runs it when "scan on startup" is on, and the two can't share one switch. Nothing uses the new gate yet.

This change also fixes the formatting of a test file added with `OnStartWhen` and `WindowWhen`.
