## Mask the remaining secrets returned by `GET /api/v1/config`

`UpdateService.MaskSecrets` now covers the five scalar secret fields and
`metadata_sources[].credentials`. These are still returned in full cleartext:

- [ ] `OAuthGithubClientSecret` and `OAuthGoogleClientSecret` (`internal/config/config.go:895-898`)
- [ ] `DelugeWebPassword` (`internal/config/config.go:991`)
- [ ] `DownloadClient.Torrent.Deluge.Password` (`config.go:172`)
- [ ] `DownloadClient.Torrent.QBittorrent.Password` (`config.go:180`)
- [ ] `DownloadClient.Usenet.Sabnzbd.APIKey` (`config.go:188`)

`ABSJWTSecret` is correctly excluded via `json:"-"` — leave it alone.

**Do not just add these to `MaskSecrets`.** Masking a field that a client sends
back makes `PUT /api/v1/config` destructive unless the echoed mask is rejected,
and `MaskSecret` is idempotent, so the response looks identical whether the
secret survived or was wiped — the failure is invisible until the integration
starts returning 401. For each field, trace which client path resends it, then
protect it the way the metadata-source credentials are protected
(`restoreMaskedCredentials`) or the scalars are (`acceptSecretUpdate`), and
mutation-test the call site rather than only the helper.
