### Fixed

- **`/api/authors`, `/api/series` and `/api/playlists` 404'd instead of reaching the app
  API.** The previous change (#2332) added six unimplemented Audiobookshelf namespaces to
  the list of paths excluded from the `/api/*` → `/api/v1/*` compatibility redirect, so
  they would answer an honest 404 to ABS clients instead of redirecting them into a
  foreign JSON shape. Three of those six — authors, series and playlists — are namespaces
  the **app API really serves** under `/api/v1` (19, 18 and 9 routes). For those the
  redirect was not a lie, and excluding them 404'd 46 working routes' unversioned form.
  Because the redirect middleware is not gated on `ABS_API_ENABLED`, this happened on
  every deployment, including ones with the ABS surface switched off. The three colliding
  namespaces now keep their redirect; `collections`, `users` and `podcasts` — which have
  no `/api/v1` twin — still 404 honestly. No unversioned traffic to any of the six was
  observed in 30 days of production logs, so no client is known to have been affected.
