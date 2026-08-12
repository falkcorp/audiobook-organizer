### Fixed

#### The scheduler dropped enabled tasks without a word — and nothing had been scanning

A task only got a timer when it was both enabled and had a non-zero interval. When it
was enabled but its interval resolved to zero, it got no timer, no log line, and no
other trace. From the outside that is indistinguishable from a task somebody turned off
on purpose.

That is how automatic library scanning stayed off without anyone noticing. The periodic
scan ships enabled with a six-hour interval, but a stored zero was overriding that
default, so the one job responsible for noticing newly added books was never scheduled —
and the only symptom was a log line that was not there. Four unrelated jobs were
scheduled normally, so the scheduler looked healthy.

The scheduler now says which task it dropped and why, and names the setting to change.
Correctly configured tasks log as before, and tasks that are off on purpose stay quiet —
so "off deliberately" and "on but broken" no longer look the same.

The underlying cause, where stored zero values permanently shadow shipped defaults, is
written up with the measurements and the options for fixing it properly.
