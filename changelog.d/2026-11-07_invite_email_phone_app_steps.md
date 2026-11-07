### Changed

- Invitations: when accepting will mint the guest's own phone-app key, the invite email now has setup steps for AudioBooth (iPhone) and Lissen (Android 9+), telling the guest to add the key as custom headers before signing in. Android names Lissen because the official Audiobookshelf Android app (v0.14.2-beta) can't send custom headers on its first request, the API, streaming or downloads, so Cloudflare Access blocks it.
