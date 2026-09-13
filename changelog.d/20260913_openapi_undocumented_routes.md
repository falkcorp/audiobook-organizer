### Fixed

- `docs/api/openapi.json` now documents 13 live routes it was missing:
  `GET /maintenance/jobs` and `POST /maintenance/wipe` (#2844), and
  `/users/invite`, `/users/invites`, `/users/invites/{token}`,
  `/auth/accept-invite`, `/deluge/status`, `/deluge/test-connection`,
  `/itunes/rebuild`, `/itunes/write-back-all`, `/users/{id}/deactivate`,
  `/users/{id}/reactivate` and `/users/{id}/reset-password` (#2846). Request
  and response shapes come from the handlers, including the `data` envelope.
  The 11 bare group-relative stubs (`/invite`, `/status`, `/rebuild`, ...)
  that stood in for the prefixed paths are removed. Spec `info.version` is
  0.219.2.
