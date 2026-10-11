- [ ] **MASKSECRET-REVEALS-TOO-MUCH** `database.MaskSecret`
      (`internal/database/settings.go:313`) masks a secret of 8 or more characters
      as its first 3 and last 4 characters around `****`, so an 8-character
      password shows 7 of its 8 characters in `GET /api/v1/config` (the Deluge web
      password, the OAuth client secrets, the download-client credentials and
      the API keys all go through it). Decide on a mask that reveals a fixed
      small amount regardless of length (for example only a length-independent
      `****`, or the last 2 characters of a secret of 20 or more), and update
      `restoreRoundTripSecrets` and the mask-comparison callers together, since
      they match a payload against the mask this function produced. Found
      reviewing the Deluge settings lock (#3966); not fixed there.
