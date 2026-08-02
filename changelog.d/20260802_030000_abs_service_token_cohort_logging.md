<!-- file: changelog.d/20260802_030000_abs_service_token_cohort_logging.md -->
<!-- version: 1.0.0 -->
<!-- guid: e5a91c26-0f74-4b38-8d62-3c17b0e4a95f -->
<!-- last-edited: 2026-08-02 -->

### Added

- **Cloudflare service-token cohort logging.** The owner mints several group-scoped
  service tokens (friends, family, other, testing) so that revoking one only affects
  that group. Each token's JWT carries a stable `common_name` (its Client ID) and no
  email, so until now a service-token request left no trace of *which* token it was.

  The ABS audit log now records `service_token=<common_name>` on:
  - **failed** authentication attempts, where it is the only attribution that will
    ever exist — an anonymous assertion never becomes a `user_id`; and
  - a **token↔person pairing** record emitted the first time a given token is seen
    carrying a given SSO identity, and again whenever that pairing **changes**.

  The pairing is deliberately on the **same line** as `user_id`/`username`. Token↔person
  is normally stable, so the `family` token suddenly carrying a friend's SSO identity
  is a tripwire for either a compromised Google account or a leaked token — and that
  anomaly is only visible when both values appear together. Emitting them separately
  would destroy the signal.

  Logged on first-seen and on change rather than per request: the ABS surface is polled
  every 15–20 s per device, so an unconditional line would be journal noise (the same
  reason `ABS_AUTH_PROBE` is opt-in). Steady state is one line per (token, person) per
  process lifetime.

  🔴 **`common_name` is never used to resolve a user.** It names a *credential* shared
  by a group of people, not a person; identity continues to come from SSO alone. The
  restriction is documented at every layer it passes through.

  Implementation note: `oauth.ErrNonIdentityAssertion` is now returned as a typed
  `*oauth.NonIdentityAssertionError` carrying the `common_name`. It still satisfies
  `errors.Is(err, ErrNonIdentityAssertion)` — a regression test pins this, because the
  fall-through branch that depends on it is what lets a Mode B request (service token
  at the edge + our own bearer) authenticate at all.
