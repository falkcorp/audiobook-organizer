### Added

#### API keys take a custom expiry date, up to three years

The Create API Key dialog keeps its 30/60/90/180/365-day presets and adds a
"Custom date" choice: a date picker that runs from tomorrow to today plus 1096
days, so the three-year anniversary is always pickable, even across a leap
day. The resulting expiry is shown before Create and announced to screen
readers.

The dialog sends the picked date as `expires_in_days`, so the server counts it
from its own clock and a browser clock that runs ahead cannot get the latest
date refused. The count is chosen so the key expires on the picked date in the
viewer's time zone, even across a daylight-saving change. The one exception is
the 23-hour spring-forward day picked within an hour of midnight: no whole-day
count can land on it, so the key then expires within the hour after that date
ends.

`POST /api/v1/auth/api-keys` now allows `expires_in_days` up to 1096 (was 365)
and also takes `expires_at`, an RFC 3339 instant. `expires_at` must be in the
future and no more than 1096 days away. Sending both fields is a 400, even
with `expires_in_days: 0`. The other rules are unchanged: a key created by a
key is still shortened to its creator's expiry, scopes must still be a subset,
the bootstrap key keeps its 8h/24h cap, and there is still no "never expires".

Core's `core.auth.apikey.max_ttl` default rises from 8760h to 26304h, and any
configured value is capped at 26304h. `default_ttl` is now held to `max_ttl`,
so a key created without an expiry can no longer outlive a lowered maximum.
The cap therefore survives the identity flip (F4-08).
