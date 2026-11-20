### Added

#### API keys take a custom expiry date, up to three years

The Create API Key dialog keeps its 30/60/90/180/365-day presets and adds a
"Custom date" choice: a date picker that runs from tomorrow to today plus 1095
days, with the resulting expiry shown before Create. The dialog sends the
picked date as `expires_in_days`, so the server counts it from its own clock.

`POST /api/v1/auth/api-keys` now allows `expires_in_days` up to 1095 (was 365)
and also takes `expires_at`, an RFC 3339 instant. `expires_at` must be in the
future and no more than 1095 days away, and sending both fields is a 400. Three
years means 1095 days, not three calendar years, which can be 1096 days across
a leap day. The other rules are unchanged: a key created by a key is still
shortened to its creator's expiry, scopes must still be a subset, the
bootstrap key keeps its 8h/24h cap, and there is still no "never expires".

Core's `core.auth.apikey.max_ttl` default rises from 8760h to 26280h, and any
configured value is now capped at 26280h, so the cap survives the identity
flip (F4-08).
