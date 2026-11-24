### Added

- The invite page (`/invite#token=...`) claims the guest's one-time phone-app key after core's accept succeeds: one `POST /api/v1/auth/phone-key/claim` (same origin, session cookie, no body, no query), made by `web/src/services/phoneKeyClaim.ts`. An issued key shows the client ID and secret with their header names and copy buttons, and says plainly that it will not be shown again. An unavailable key shows its message and tells the guest to ask the administrator to reissue it. A 404 (a link-only invite), 401, 403, 429, any other answer or a network error shows nothing about a phone key, is not retried and does not turn the accepted invitation into an error. The secret lives in component state only: never in web storage, the URL, a query cache or a log.

### Changed

- Core's invite accept no longer feeds the phone-app key to the page; `acceptInvite` returns nothing and the claim is the one source of the key.
