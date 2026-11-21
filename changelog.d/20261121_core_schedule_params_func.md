### Added

#### Core task schedules can build their parameters at each run: `ParamsFunc`

A core task schedule can now read its run parameters from current settings each time it starts, not once when the app starts. Two of today's scheduled jobs need this to behave as they do now: the online AcoustID lookup reads its nightly limit, and the candidate fetch reads the "identification advance" switch, each time they run. Without it, changing either setting would only take effect after a restart. Nothing uses the new option yet.
