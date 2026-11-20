<!-- file: docs/agent-tasks/platform-core/seams.d/F4-08-apikey-expiry.md -->
<!-- version: 0.1.0 -->
<!-- guid: ec380bc4-5b16-41de-868e-e628dc9f1b62 -->
<!-- last-edited: 2026-11-20 -->

# F4-08: API key custom expiry and the three-year cap

This is the owner request of 2026-11-20: an API key can have a custom expiry date, at most three years out. It landed on HEAD's gin route and in core before the flip. F4-08 moves the SPA's key calls onto core's routes and must not drop the cap or the custom date.

## What HEAD does now

- `POST /api/v1/auth/api-keys` (`internal/server/handlers/apikeys.go`):
  - `expires_in_days` runs from 1 to 1096. 0 or omitted is the 30-day default.
  - `expires_at` is an RFC 3339 instant. It must be after the request and no more than 1096 × 24 h after it.
  - Sending both fields is a 400, even with `expires_in_days: 0` (the field is a `*int`).
  - A key caller is still clamped to its own expiry, after the cap check.
- "Three years" is **1096 × 24 h everywhere**, so the anniversary date is always pickable, even across a leap day (owner ruling, 2026-11-20). That holds for HEAD's handler, the SPA picker and core's `max_ttl`.
- The SPA (`web/src/components/settings/APIKeysTab.tsx`, `web/src/utils/apiKeyExpiry.ts`):
  - The presets stay, and "Custom date" adds a native date input bounded from tomorrow to today + 1096 days.
  - A custom date is sent as `expires_in_days`, never as `expires_at`. The server counts the days from its own clock, so the latest date cannot be refused because the browser's clock runs ahead.
  - `customExpiryDays` picks the day count so that now + days × 24 h lands on the picked local date across a daylight-saving change. The one exception is the 23-hour spring-forward day picked within an hour of midnight; the key then expires within the hour after that date ends.
  - vitest runs in `America/New_York` (`web/vitest.config.ts`), so the DST cells test something.

## What core does now

- `auth.DefaultKeyMaxTTL` is 1096 × 24 h (was 8760h), and the kernel's `core.auth.apikey.max_ttl` default is 26304h.
- New `auth.MaxKeyMaxTTL` (also 1096 × 24 h) caps any configured `max_ttl` in `Settings.withDefaults`, the way `MaxBootstrapKeyTTL` caps `bootstrap.key_ttl`. A longer `ExpiresIn` is `ErrBadInput`.
- `withDefaults` also holds `KeyDefaultTTL` to `KeyMaxTTL`. Before this, `CreateAPIKey`'s `ExpiresIn == 0` path took the default without the maximum check.
- Tests: `core/auth/apikey_maxttl_test.go` (`TestAPIKey_MaxTTLIsThreeYears`, `TestAPIKey_DefaultTTLNeverExceedsMaxTTL`).
- Not changed: the key-actor clamp to its own key's expiry (`CreateAPIKey`), the bootstrap key's 8 h default and 24 h ceiling, and the scope-subset checks.

## What F4-08 must carry

1. **SPA mapping.** When `APIKeysTab` moves to `POST /api/core/v1/me/api-keys` (`authhttp.KeyCreate`), send the dialog's day count as `expires_in: "<days*24>h"`. That covers both a preset and a custom date. Keep `customExpiryBounds`, `customExpiryDays` and `API_KEY_MAX_TTL_DAYS`, and keep their vitest cells (`apiKeyExpiry.test.ts`, `APIKeysTab.test.tsx`).
2. **`expires_at` is not on core's route.** `authhttp.KeyCreate` takes only `expires_in`, and nothing first-party sends `expires_at` (the SPA sends days). Deleting the gin route drops `expires_at` for scripted callers. Either add an `expires_at` field to `KeyCreate` that maps to `ExpiresIn = at - now`, with the same future and ≤ 1096 d checks, and regenerate the openapi and client in F4-08 as you already will, or list the drop in the flip's owner notice. This PR does not regenerate `core/openapi` or the generated client.
3. **Do not lower `max_ttl` below 26304h** in ABO's config layers. A 1096-day key from the SPA would then be refused at core.
4. **Imported keys:** M3 keeps an imported `abk_` key's `ExpiresAt` as stored (`internal/books/cutover/m3/mapping.go`), with no bound. A key created on HEAD with up to 1096 days left migrates unchanged. That is no new blocker, since nothing caps an imported key's remaining life.
