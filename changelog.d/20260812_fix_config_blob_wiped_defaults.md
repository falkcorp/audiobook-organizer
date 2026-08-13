### Fixed

#### Saved settings quietly erased every shipped default

Configuration is stored as a single saved snapshot. On startup that snapshot was applied
by replacing the entire configuration with it — so any setting the snapshot did not
mention was not left at its shipped default, it was reset to empty or zero.

Because the snapshot is written once and re-read on every start, this made the erasure
permanent: every setting added after that snapshot was saved stayed at zero forever, and
no later change to a default could ever reach the installation.

That is why nothing was scanning for newly added audiobooks. Automatic library scanning
ships switched on with a six-hour interval, but the saved snapshot predated it, so on
every startup it came back switched off with an interval of zero. The feature was
present and working; the saved settings were erasing it before it ever ran.

Saved settings are now applied on top of the defaults rather than in place of them, so a
setting the snapshot does not mention keeps the value it shipped with. Anything
deliberately turned off stays off — those choices are recorded in the snapshot and still
take precedence.

Because this means some settings will pick up their intended defaults for the first
time, startup now lists exactly which ones did, so the change is visible rather than
something to discover later.
