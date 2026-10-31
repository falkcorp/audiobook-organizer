### Fixed

- `database.MaskSecret` no longer reveals most of a short secret in
  `GET /api/v1/config`. A secret under 16 characters now masks to exactly
  `********`; a secret of 16 or more shows only its last 2 characters after
  `********`. No prefix is ever shown (previously an 8-character password
  displayed 7 of its 8 characters). The round-trip guards compare against
  `MaskSecret(stored)`, so a client echoing the masked value back still keeps
  the stored secret.
